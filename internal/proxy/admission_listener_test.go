package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type e2ePendingDial struct{ started, canceled chan struct{} }

func (d *e2ePendingDial) DialPort(ctx context.Context, _ uint16) (net.Conn, error) {
	close(d.started)
	<-ctx.Done()
	close(d.canceled)
	return nil, ctx.Err()
}

type e2eResetConn struct {
	net.Conn
	reset <-chan struct{}
}

func (c *e2eResetConn) Read([]byte) (int, error) { <-c.reset; return 0, syscall.ECONNRESET }

type e2eSingleListener struct {
	conn    net.Conn
	pending bool
	done    chan struct{}
	once    sync.Once
}

func (l *e2eSingleListener) Accept() (net.Conn, error) {
	if !l.pending {
		l.pending = true
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *e2eSingleListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *e2eSingleListener) Addr() net.Addr { return l.conn.LocalAddr() }

// A reset is terminal, unlike a valid TCP half-close. A normal app abort
// during lazy dial must cancel only that admission and return its reservation.
func TestE2EReviewResetDuringAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		a, b := net.Pipe()
		defer b.Close()
		reset := make(chan struct{})
		l := &e2eSingleListener{conn: &e2eResetConn{a, reset}, done: make(chan struct{})}
		d := &e2ePendingDial{make(chan struct{}), make(chan struct{})}
		finished := make(chan error, 1)
		go func() { finished <- Serve(ctx, d, []Forward{{Listener: l, Port: 445}}, 1, nil) }()
		defer func() {
			cancel()
			if err := <-finished; err != nil {
				t.Error(err)
			}
		}()
		<-d.started
		close(reset)
		synctest.Wait()
		select {
		case <-d.canceled:
		default:
			t.Error("client reset left DialPort pending under the daemon context")
		}
	})
}

// Scripted reads exercise n > 0 together with EOF, as well as several staged
// chunks. Writes still use the pipe, so the response direction is independent.
type admissionReadConn struct {
	net.Conn
	data     []byte
	terminal error
	reads    int
}

func (c *admissionReadConn) Read(p []byte) (int, error) {
	c.reads++
	n := copy(p, c.data)
	c.data = c.data[n:]
	if len(c.data) == 0 {
		return n, c.terminal
	}
	return n, nil
}

type admissionHalfConn struct {
	net.Conn
	end chan struct{}
}

func (c *admissionHalfConn) CloseWrite() error { close(c.end); return nil }

type admissionDialRequest struct {
	ctx    context.Context
	result chan net.Conn
}
type admissionGateDial struct{ requests chan admissionDialRequest }

func (d admissionGateDial) DialPort(ctx context.Context, _ uint16) (net.Conn, error) {
	r := admissionDialRequest{ctx, make(chan net.Conn)}
	d.requests <- r
	select {
	case c, ok := <-r.result:
		if !ok {
			return nil, net.ErrClosed
		}
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestAdmissionStagedBytesAndHalfClose(t *testing.T) {
	for _, size := range []int{0, 7, 12000, 100000} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				a, client := net.Pipe()
				defer client.Close()
				want := make([]byte, size)
				for i := range want {
					want[i] = byte(i * 37)
				}
				local := &admissionReadConn{Conn: a, data: bytes.Clone(want), terminal: io.EOF}
				l := &e2eSingleListener{conn: local, done: make(chan struct{})}
				d := admissionGateDial{make(chan admissionDialRequest)}
				finished := make(chan error, 1)
				go func() { finished <- Serve(ctx, d, []Forward{{Listener: l, Port: 445}}, 1, nil) }()
				defer func() {
					cancel()
					if err := <-finished; err != nil {
						t.Error(err)
					}
				}()
				r := <-d.requests
				synctest.Wait()
				if local.reads == 0 {
					t.Error("accepted client was not read while admission was pending")
				}
				if err := r.ctx.Err(); err != nil {
					t.Fatalf("valid pending input canceled admission: %v", err)
				}
				remote, device := net.Pipe()
				defer device.Close()
				device.SetReadDeadline(time.Now().Add(time.Second))
				end := make(chan struct{})
				r.result <- &admissionHalfConn{remote, end}
				got := make([]byte, size)
				if _, err := io.ReadFull(device, got); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatal("staged and subsequent bytes differ from input")
				}
				<-end
				responseDone := make(chan struct{})
				go func() { defer close(responseDone); device.Write([]byte("response")); device.Close() }()
				response, err := io.ReadAll(client)
				if err != nil || string(response) != "response" {
					t.Fatalf("response after FIN = %q, %v", response, err)
				}
				<-responseDone
			})
		})
	}
}

func TestAdmissionResetReclaimsSessionReservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		a, b := net.Pipe()
		defer b.Close()
		reset := make(chan struct{})
		l := &e2eSingleListener{conn: &e2eResetConn{a, reset}, done: make(chan struct{})}
		s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 257), done: make(chan struct{})}
		finished := make(chan error, 1)
		go func() { finished <- Serve(ctx, s, []Forward{{Listener: l, Port: 445}}, 1, nil) }()
		defer func() {
			cancel()
			if err := <-finished; err != nil {
				t.Error(err)
			}
		}()
		admitted := <-s.in
		if admitted.Kind != session.Client {
			t.Fatalf("first command = %v, want Client", admitted.Kind)
		}
		close(reset)
		synctest.Wait()
		select {
		case gone := <-s.in:
			if gone.Kind != session.ClientGone || gone.Client != admitted.Client {
				t.Fatalf("cleanup command = %+v", gone)
			}
			s.Observe(gone, session.Snapshot{}, session.Snapshot{}, core.Instant(0))
			if len(s.clients) != 0 {
				t.Fatal("consumed cancellation retained registration")
			}
		default:
			t.Error("client reset did not queue Session ClientGone")
		}
	})
}

// Read deliberately outlives Close until released, making the worker join
// observable without a process-wide goroutine count or scheduler timing.
type admissionJoinConn struct {
	net.Conn
	reading, closed, release, exited chan struct{}
	once                             sync.Once
}

func (c *admissionJoinConn) Read([]byte) (int, error) {
	close(c.reading)
	<-c.closed
	<-c.release
	close(c.exited)
	return 0, net.ErrClosed
}
func (c *admissionJoinConn) Close() error {
	c.once.Do(func() { close(c.closed); c.Conn.Close() })
	return nil
}
func TestAdmissionJoinsPrereadOnExit(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(fmt.Sprint(reject), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				a, b := net.Pipe()
				defer b.Close()
				local := &admissionJoinConn{Conn: a, reading: make(chan struct{}), closed: make(chan struct{}), release: make(chan struct{}), exited: make(chan struct{})}
				l := &e2eSingleListener{conn: local, done: make(chan struct{})}
				d := admissionGateDial{make(chan admissionDialRequest)}
				finished := make(chan error, 1)
				go func() { finished <- Serve(ctx, d, []Forward{{Listener: l, Port: 445}}, 1, nil) }()
				r := <-d.requests
				synctest.Wait()
				select {
				case <-local.reading:
				default:
					t.Error("no preread worker during pending dial")
					cancel()
					close(local.release)
					<-finished
					return
				}
				if reject {
					// Reject this admission while the listener stays live.
					close(r.result)
				} else {
					cancel()
				}
				<-local.closed
				synctest.Wait()
				select {
				case err := <-finished:
					t.Fatalf("Serve returned before preread exited: %v", err)
				default:
				}
				close(local.release)
				<-local.exited
				cancel()
				if err := <-finished; err != nil {
					t.Error(err)
				}
			})
		})
	}
}

type admissionQueueListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *admissionQueueListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *admissionQueueListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *admissionQueueListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestAdmissionResetLeavesSiblingAndReturnsSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		a, b := net.Pipe()
		defer b.Close()
		sibling, siblingPeer := net.Pipe()
		defer siblingPeer.Close()
		reset := make(chan struct{})
		l := &admissionQueueListener{conns: make(chan net.Conn), done: make(chan struct{})}
		d := admissionGateDial{make(chan admissionDialRequest)}
		finished := make(chan error, 1)
		go func() { finished <- Serve(ctx, d, []Forward{{Listener: l, Port: 445}}, 2, nil) }()
		defer func() {
			cancel()
			if err := <-finished; err != nil {
				t.Error(err)
			}
		}()
		l.conns <- &e2eResetConn{a, reset}
		first := <-d.requests
		l.conns <- sibling
		second := <-d.requests
		close(reset)
		synctest.Wait()
		if first.ctx.Err() == nil {
			t.Error("reset admission still live")
		}
		if err := second.ctx.Err(); err != nil {
			t.Fatalf("sibling canceled by reset: %v", err)
		}
		remote, device := net.Pipe()
		defer device.Close()
		second.result <- remote
		copied := make(chan error, 1)
		go func() { _, err := siblingPeer.Write([]byte("sibling")); copied <- err }()
		got := make([]byte, 7)
		if _, err := io.ReadFull(device, got); err != nil || string(got) != "sibling" {
			t.Fatalf("sibling data=%q error=%v", got, err)
		}
		if err := <-copied; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		replacement, peer := net.Pipe()
		defer peer.Close()
		l.conns <- replacement
		synctest.Wait()
		select {
		case third := <-d.requests:
			if err := third.ctx.Err(); err != nil {
				t.Fatalf("replacement canceled: %v", err)
			}
		default:
			t.Error("reset admission did not return listener slot")
		}
	})
}
