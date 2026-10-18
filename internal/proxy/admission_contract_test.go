package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestAdmissionStagingBoundAndSaturatedShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a, b := net.Pipe()
		defer b.Close()
		const size = 1 << 20
		local := &admissionReadConn{Conn: a, data: make([]byte, size), terminal: io.EOF}
		l := &e2eSingleListener{conn: local, done: make(chan struct{})}
		d := admissionGateDial{make(chan admissionDialRequest)}
		finished := make(chan error, 1)
		go func() { finished <- Serve(ctx, d, []Forward{{Listener: l, Port: 445}}, 1, nil) }()
		<-d.requests
		synctest.Wait()
		if staged := size - len(local.data); staged <= 0 || staged > 16<<10 {
			t.Errorf("pending read retained %d bytes, want 1..16384", staged)
		}
		cancel()
		if err := <-finished; err != nil {
			t.Error(err)
		}
		reads := local.reads
		synctest.Wait()
		if local.reads != reads {
			t.Error("preread continued after Serve joined")
		}
	})
}

func TestAdmissionBytesWithTerminalErrorCancelDial(t *testing.T) {
	for _, readErr := range []error{syscall.ECONNRESET, net.ErrClosed, io.ErrUnexpectedEOF} {
		t.Run(readErr.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				a, b := net.Pipe()
				defer b.Close()
				local := &admissionReadConn{Conn: a, data: []byte{0, 1, 2, 255}, terminal: readErr}
				l := &e2eSingleListener{conn: local, done: make(chan struct{})}
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
				synctest.Wait()
				select {
				case <-d.canceled:
				default:
					t.Error("bytes together with terminal error left admission pending")
				}
			})
		})
	}
}

// Listener cancellation belongs to admission. After ownership is handed off,
// stream.Close, rather than the former admission context, retires the channel.
func TestSessionAdmissionContextEndsAtHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 257), done: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		type result struct {
			conn net.Conn
			err  error
		}
		dialled := make(chan result, 1)
		go func() { conn, err := s.DialPort(ctx, 445); dialled <- result{conn, err} }()
		e := <-s.in
		if e.Kind != session.Client {
			t.Fatalf("first command = %v, want Client", e.Kind)
		}
		s.mu.Lock()
		c := s.clients[e.Client]
		c.result <- nil
		s.mu.Unlock()
		got := <-dialled
		if got.conn != c || got.err != nil {
			t.Fatalf("admission result = %v, %v", got.conn, got.err)
		}
		cancel()
		synctest.Wait()
		select {
		case <-c.done:
			t.Fatal("admission context closed handed-off stream")
		default:
		}
		select {
		case e := <-s.in:
			t.Fatalf("admission cancellation queued %+v after handoff", e)
		default:
		}
		if err := c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
		gone := <-s.in
		if gone.Kind != session.ClientGone || gone.Client != c.id {
			t.Fatalf("Close queued %+v, want ClientGone", gone)
		}
	})
}
