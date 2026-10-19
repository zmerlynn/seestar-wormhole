package proxy

import (
	"context"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/pion/transport/v5/deadline"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type delivery struct{ action p2ptunnel.Action }

// stream holds at most one write action and one read reservation. Read/Write
// mutexes implement net.Conn's concurrency contract, never protocol ownership.
type stream struct {
	s                  *Session
	id                 uint64
	port               uint16
	channel            byte // immutable before successful admission is published
	result             chan error
	rx                 chan delivery
	ready              chan p2ptunnel.Action
	done, readEnd      chan struct{}
	closeOnce, endOnce sync.Once
	readMu, writeMu    sync.Mutex
	writeEnd           bool
	forgotten          bool      // s.mu: cleanup queued; registration remains reserved
	terminal           error     // published by closing done
	pending            *delivery // readMu
	consumed           int
	rd, wd             *deadline.Deadline
	request            func(core.Instant)                  // owner-only demand for a read reservation
	step               func(p2ptunnel.Event, core.Instant) // accessed only by the session owner
}

func newStream(s *Session, id uint64, port uint16) *stream {
	return &stream{s: s, id: id, port: port, result: make(chan error, 1), rx: make(chan delivery, 1), ready: make(chan p2ptunnel.Action, 1), done: make(chan struct{}), readEnd: make(chan struct{}), rd: deadline.New(), wd: deadline.New()}
}
func (c *stream) finish(err error) {
	if err == nil {
		err = session.ErrClosed
	}
	select {
	case c.result <- err:
	default:
	}
	c.closeOnce.Do(func() { c.terminal = err; close(c.done); c.rd.Set(time.Time{}); c.wd.Set(time.Time{}) })
}
func event(a p2ptunnel.Action, k p2ptunnel.EventKind) p2ptunnel.Event {
	return p2ptunnel.Event{Kind: k, Channel: a.Channel, Generation: a.Generation, Token: a.Token}
}
func (c *stream) submit(e p2ptunnel.Event, limit <-chan struct{}) error {
	select {
	case <-c.done:
		return net.ErrClosed
	case <-limit:
		return os.ErrDeadlineExceeded
	default:
	}
	select {
	case c.s.work <- func(now core.Instant) {
		if c.step != nil {
			c.step(e, now)
		}
	}:
		return nil
	case <-c.done:
		return net.ErrClosed
	case <-limit:
		return os.ErrDeadlineExceeded
	case <-c.s.done:
		return net.ErrClosed
	}
}
func (c *stream) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(b) == 0 {
		return 0, nil
	}
	select {
	case <-c.rd.Done():
		return 0, os.ErrDeadlineExceeded
	default:
	}
	// Drain already delivered bytes before reporting a terminal cause.
	if c.pending == nil {
		select {
		case d := <-c.rx:
			c.pending = &d
		default:
		}
	}
	if c.pending == nil {
		var terminal error
		select {
		case d := <-c.rx:
			c.pending = &d
		case <-c.done:
			terminal = c.terminal
		case <-c.readEnd:
			terminal = io.EOF
			select {
			case <-c.done:
				terminal = c.terminal
			default:
			}
		case <-c.rd.Done():
			return 0, os.ErrDeadlineExceeded
		}
		if terminal != nil {
			// The owner publishes deliveries before END/Close. A select can
			// choose closure over a ready delivery, so recheck after acquiring
			// that publication before exposing the terminal cause.
			select {
			case d := <-c.rx:
				c.pending = &d
			default:
				return 0, terminal
			}
		}
	}
	d := c.pending
	n := copy(b, d.action.Data)
	d.action.Data = d.action.Data[n:]
	c.consumed += n // at most MaxData; accumulated completion count
	if len(d.action.Data) == 0 {
		e := event(d.action, p2ptunnel.LocalWritten)
		e.Count = c.consumed
		c.pending = nil
		c.consumed = 0
		// Completion is flow control. Only the caller blocks, and shutdown unblocks it.
		select {
		case c.s.work <- func(now core.Instant) { c.step(e, now) }:
		case <-c.done:
		case <-c.s.done:
		}
	}
	return n, nil
}
func (c *stream) reservation() (p2ptunnel.Action, error) {
	// Prefer an existing reservation retained across a timed-out write.
	select {
	case <-c.wd.Done():
		return p2ptunnel.Action{}, os.ErrDeadlineExceeded
	default:
	}
	select {
	case a := <-c.ready:
		return a, nil
	default:
	}
	select {
	case c.s.work <- func(now core.Instant) {
		if c.request != nil {
			c.request(now)
		}
	}:
	case <-c.done:
		return p2ptunnel.Action{}, net.ErrClosed
	case <-c.wd.Done():
		return p2ptunnel.Action{}, os.ErrDeadlineExceeded
	}

	select {
	case <-c.done:
		return p2ptunnel.Action{}, net.ErrClosed
	case <-c.wd.Done():
		return p2ptunnel.Action{}, os.ErrDeadlineExceeded
	default:
	}
	select {
	case a := <-c.ready:
		return a, nil
	case <-c.done:
		return p2ptunnel.Action{}, net.ErrClosed
	case <-c.wd.Done():
		return p2ptunnel.Action{}, os.ErrDeadlineExceeded
	}
}
func (c *stream) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeEnd {
		return 0, net.ErrClosed
	}
	count := 0
	for len(b) > 0 {
		a, err := c.reservation()
		if err != nil {
			return count, err
		}
		n := min(len(b), p2ptunnel.MaxData)
		e := event(a, p2ptunnel.LocalData)
		e.Data = append([]byte(nil), b[:n]...)
		if err = c.submit(e, c.wd.Done()); err != nil {
			// A timeout before handoff retains the one read reservation for retry.
			select {
			case c.ready <- a:
			default:
			}
			return count, err
		}
		count += n
		b = b[n:]
	}
	return count, nil
}

// CloseWrite propagates END while leaving the receive direction usable.
func (c *stream) CloseWrite() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeEnd {
		return nil
	}
	a, err := c.reservation()
	if err != nil {
		return err
	}
	if err = c.submit(event(a, p2ptunnel.LocalEOF), c.wd.Done()); err != nil {
		select {
		case c.ready <- a:
		default:
		}
		return err
	}
	c.writeEnd = true
	return nil
}
func (c *stream) Close() error                       { c.finish(net.ErrClosed); c.s.forget(c.id); return nil }
func (c *stream) LocalAddr() net.Addr                { return streamAddr{0} }
func (c *stream) RemoteAddr() net.Addr               { return streamAddr{c.port} }
func (c *stream) SetDeadline(t time.Time) error      { c.rd.Set(t); c.wd.Set(t); return nil }
func (c *stream) SetReadDeadline(t time.Time) error  { c.rd.Set(t); return nil }
func (c *stream) SetWriteDeadline(t time.Time) error { c.wd.Set(t); return nil }

type streamAddr struct{ port uint16 }

func (streamAddr) Network() string { return "p2ptunnel" }
func (streamAddr) String() string  { return "device-stream" }

// Compile-time internal interface checks include the half-close extension consumed by TCP forwarding.
var _ net.Conn = (*stream)(nil)
var _ interface{ CloseWrite() error } = (*stream)(nil)
var _ interface {
	DialPort(context.Context, uint16) (net.Conn, error)
} = (*Session)(nil)
