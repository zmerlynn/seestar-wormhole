package proxy

import (
	"context"
	"net"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Model kernel error state separately from the unread stream, just as SO_ERROR
// does. The test triggers reset only after the byte-reader is blocked staging.
type saturatedResetConn struct {
	*admissionReadConn
	reset chan struct{}
}

func (c *saturatedResetConn) socketError() error {
	select {
	case <-c.reset:
		return syscall.ECONNRESET
	default:
		return nil
	}
}

// No timing: a 16 KiB prefix fills the bounded preread queue before a reset.
// This is an ordinary request followed by an abort, not a resource flood.
func TestSaturatedResetRemovesLazyDemand(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a, b := net.Pipe()
		defer b.Close()
		local := &saturatedResetConn{&admissionReadConn{Conn: a, data: make([]byte, 3*admissionReadSize), terminal: syscall.ECONNRESET}, make(chan struct{})}
		l := &e2eSingleListener{conn: local, done: make(chan struct{})}
		s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 257), done: make(chan struct{})}
		finished := make(chan error, 1)
		go func() { finished <- Serve(ctx, s, []Forward{{Listener: l, Port: 445}}, 1, nil) }()
		defer func() {
			cancel()
			if err := <-finished; err != nil {
				t.Error(err)
			}
		}()
		first := <-s.in
		cfg := session.DefaultConfig()
		cfg.Idle = time.Second
		m, err := session.New(cfg, admissionRNG{})
		if err != nil {
			t.Fatal(err)
		}
		m.Step(session.Event{Kind: session.Start}, 0)
		m.Step(first, 1)
		synctest.Wait()
		t.Logf("staged=%d unread=%d registered=%d", 3*admissionReadSize-len(local.data), len(local.data), len(s.clients))
		if len(local.data) != admissionReadSize || local.reads != 2 || m.Snapshot().Clients != 1 {
			t.Fatal("reset must follow full staging and registered lazy demand")
		}
		close(local.reset)
		// Advance fake time to the bounded socket check; no scheduler delay.
		<-time.After(10 * time.Millisecond)
		synctest.Wait()
		select {
		case gone := <-s.in:
			if gone.Kind != session.ClientGone || gone.Client != first.Client {
				t.Fatalf("got cleanup %+v", gone)
			}
			before := m.Snapshot()
			m.Step(gone, 2)
			s.Observe(gone, before, m.Snapshot(), 2)
			if m.Snapshot().Clients != 0 || m.Snapshot().Queued != 0 || len(s.clients) != 0 {
				t.Fatalf("stale lazy snapshot=%+v registrations=%d", m.Snapshot(), len(s.clients))
			}
		default:
			t.Error("reset after 24 KiB leaves lazy Client demand and listener slot pending")
		}
	})
}

type resetReadCount struct {
	net.Conn
	reads chan int
}

func (c *resetReadCount) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.reads <- n
	return n, err
}

// Preserve the underlying socket capability through the read-counting wrapper.
func (c *resetReadCount) SyscallConn() (syscall.RawConn, error) {
	return c.Conn.(syscall.Conn).SyscallConn()
}
func TestRealTCPResetAfterStaging(t *testing.T) {
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	client, err := net.DialTCP("tcp", nil, l.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	local, err := l.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counted := &resetReadCount{Conn: local, reads: make(chan int, 8)}
	single := &e2eSingleListener{conn: counted, done: make(chan struct{})}
	d := admissionGateDial{make(chan admissionDialRequest)}
	finished := make(chan error, 1)
	go func() { finished <- Serve(ctx, d, []Forward{{Listener: single, Port: 445}}, 1, nil) }()
	defer func() {
		cancel()
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}()
	req := <-d.requests
	client.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := client.Write(make([]byte, 24<<10)); err != nil {
		t.Fatal(err)
	}
	total := 0
	for i := 0; i < 2; i++ {
		select {
		case n := <-counted.reads:
			total += n
		case <-time.After(time.Second):
			t.Fatal("preread did not saturate")
		}
	}
	if err := client.SetLinger(0); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-req.ctx.Done():
	case <-time.After(250 * time.Millisecond):
		t.Errorf("real loopback RST after 24 KiB kept DialPort live (preread=%d bytes)", total)
	}
}

type admissionRNG struct{}

func (admissionRNG) Uint64() uint64 { return 0 }
