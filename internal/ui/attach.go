package ui

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"

	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// smbPort is the device port the file tools reach through a run.
const smbPort = 445

// attachments is the current run's channel to its session, which the
// account route's file tools dial through. Its mutex is a leaf lock: it
// may be taken while the controller's or the copy app's locks are held,
// no lock is taken while it is held, and it is never held over I/O,
// including a conn's Close.
type attachments struct {
	mu  sync.Mutex
	cur *attachment // the current run's record; nil before the first run

	// decide, if set, runs after a successful DialPort and before the
	// admission's decision. Tests pause there.
	decide func()
}

// attachment is one run's record: pending until the run attaches its
// session's dialer, stopping once Stop, detach or the run's end comes.
type attachment struct {
	run      uint64       // the controller's run number
	dialer   proxy.Dialer // nil until the run attaches
	ctx      context.Context
	cancel   context.CancelFunc // ends ctx; called with stopping set
	stopping bool
	conns    map[*trackedConn]struct{} // channels handed out and not closed
	wg       sync.WaitGroup            // one per admission in flight and per open channel
	regs     atomic.Int64              // admission AfterFunc registrations not yet released
}

// begin installs run's pending record, with a context derived from the
// run's, and marks any older record stopping.
func (as *attachments) begin(ctx context.Context, run uint64) {
	actx, cancel := context.WithCancel(ctx)
	as.mu.Lock()
	defer as.mu.Unlock()
	if old := as.cur; old != nil {
		old.stopping = true
		old.cancel()
	}
	as.cur = &attachment{run: run, ctx: actx, cancel: cancel, conns: map[*trackedConn]struct{}{}}
}

// attach publishes d as run's dialer. It fails, with a detach that does
// nothing, when Stop came first or a newer run replaced the record. The
// detach closes and joins every channel the run handed out.
func (as *attachments) attach(run uint64, d proxy.Dialer) (detach func(), ok bool) {
	as.mu.Lock()
	a := as.cur
	if a == nil || a.run != run || a.stopping || d == nil {
		as.mu.Unlock()
		return func() {}, false
	}
	a.dialer = d
	as.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { as.detach(a) }) }, true
}

// stop marks run's record stopping: no admission starts after it, and
// one blocked in DialPort returns.
func (as *attachments) stop(run uint64) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if a := as.cur; a != nil && a.run == run {
		a.stopping = true
		a.cancel()
	}
}

// finish clears run's record at the run's end, attached or not.
func (as *attachments) finish(run uint64) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if a := as.cur; a != nil && a.run == run {
		a.stopping = true
		a.cancel()
		as.cur = nil
	}
}

// live reports whether run is attached and not stopping.
func (as *attachments) live(run uint64) bool {
	as.mu.Lock()
	defer as.mu.Unlock()
	a := as.cur
	return a != nil && a.run == run && a.dialer != nil && !a.stopping
}

// detach ends a's run: it refuses new admissions, closes every channel
// handed out, and returns once each admission in flight has returned and
// each channel's Close has finished, whoever started it.
func (as *attachments) detach(a *attachment) {
	as.mu.Lock()
	a.stopping = true
	a.cancel()
	conns := make([]*trackedConn, 0, len(a.conns))
	for c := range a.conns {
		conns = append(conns, c)
	}
	if as.cur == a {
		as.cur = nil
	}
	as.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	a.wg.Wait()
}

// dial opens a channel to device port 445 through run's session. It
// refuses with errNotConnected unless run is attached and not stopping.
// Stop or detach ends an admission blocked in DialPort; the channel the
// admission returns lives until it is closed or the run detaches.
func (as *attachments) dial(ctx context.Context, run uint64) (net.Conn, error) {
	as.mu.Lock()
	a := as.cur
	if a == nil || a.run != run || a.dialer == nil || a.stopping {
		as.mu.Unlock()
		return nil, errNotConnected
	}
	a.wg.Add(1)
	as.mu.Unlock()

	// The admission's ctx ends with the caller's or with the run's
	// attachment. Its registration is released as soon as DialPort
	// returns: the stream outlives the admission.
	dctx, dcancel := context.WithCancel(ctx)
	a.regs.Add(1)
	ran := make(chan struct{})
	stop := context.AfterFunc(a.ctx, func() {
		dcancel()
		a.regs.Add(-1)
		close(ran)
	})
	conn, err := a.dialer.DialPort(dctx, smbPort)
	if stop() {
		a.regs.Add(-1)
	} else {
		<-ran
	}
	dcancel()
	if err != nil {
		a.wg.Done()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if a.ctx.Err() != nil {
				return nil, errNotConnected
			}
		}
		return nil, err
	}

	if as.decide != nil {
		as.decide()
	}
	as.mu.Lock()
	if a.stopping || a.ctx.Err() != nil {
		as.mu.Unlock()
		_ = conn.Close()
		a.wg.Done()
		return nil, errNotConnected
	}
	tc := &trackedConn{Conn: conn, as: as, a: a, done: make(chan struct{})}
	a.conns[tc] = struct{}{}
	as.mu.Unlock()
	return tc, nil
}

// trackedConn is a channel an attachment handed out. It holds its count
// in the attachment's wait group until its stream's Close has returned.
type trackedConn struct {
	net.Conn
	as      *attachments
	a       *attachment
	closing atomic.Bool
	done    chan struct{} // closed once the stream's Close has returned
	err     error         // the stream's Close's result; set before done
}

// Close closes the stream once, with no lock held. A concurrent or later
// Close waits for that one and returns its result.
func (c *trackedConn) Close() error {
	if c.closing.CompareAndSwap(false, true) {
		c.err = c.Conn.Close()
		c.as.mu.Lock()
		delete(c.a.conns, c)
		c.as.mu.Unlock()
		c.a.wg.Done()
		close(c.done)
	}
	<-c.done
	return c.err
}
