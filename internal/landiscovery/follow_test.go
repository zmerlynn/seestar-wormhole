package landiscovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// session is a connection Follow returns or releases.
type session struct {
	endpoint string
	closed   atomic.Bool
}

func release(s *session) { s.closed.Store(true) }

// observedConn is a scan socket whose replies a test feeds one at a time
// with deliver, which returns once the scan has handled the reply: its
// next read has begun, so the reply has passed the watcher.
type observedConn struct {
	*fakeConn
	mu        sync.Mutex
	delivered int
	want      int
	handled   chan struct{}
}

func (c *observedConn) ReadFrom(b []byte) (int, net.Addr, error) {
	c.mu.Lock()
	if c.handled != nil && c.delivered >= c.want {
		close(c.handled)
		c.handled = nil
	}
	c.mu.Unlock()
	n, from, err := c.fakeConn.ReadFrom(b)
	if err == nil {
		c.mu.Lock()
		c.delivered++
		c.mu.Unlock()
	}
	return n, from, err
}

// deliver sends replies, from the addresses given, and waits until the
// scan has handled all of them.
func (c *observedConn) deliver(t *testing.T, replies ...datagram) {
	t.Helper()
	handled := make(chan struct{})
	c.mu.Lock()
	c.want = c.delivered + len(replies)
	c.handled = handled
	c.mu.Unlock()
	for _, d := range replies {
		c.in <- d
	}
	select {
	case <-handled:
	case <-time.After(10 * time.Second):
		t.Fatal("the scan didn't handle the replies")
	}
}

// observedScan is a scan that sends once and listens for window, whose
// first send is answered with first.
func observedScan(window time.Duration, first ...datagram) (*Scanner, *observedConn) {
	c := &observedConn{fakeConn: newFakeConn()}
	c.respond = func(to netip.Addr) []datagram {
		if to != netip.MustParseAddr("192.0.2.255") {
			return nil
		}
		return first
	}
	s := scanner(c.fakeConn, window)
	s.Sends = 1
	s.Interval = time.Hour
	s.Listen = func() (Conn, error) { return c, nil }
	return s, c
}

func answer(serial, from string) datagram {
	return datagram{replyBytes(serial, "S50_A", "Seestar S50", 0), netip.MustParseAddr(from)}
}

// The saved address answering first with another serial, then with the
// saved one, is never dialed, and the search fails with an error.
func TestFollowConflictBeforeMatchingReply(t *testing.T) {
	s, _ := observedScan(100*time.Millisecond, answer("SERIAL-B", "192.0.2.20"), answer("SERIAL-A", "192.0.2.20"))
	dials := 0
	got, r, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
		dials++
		return &session{endpoint: ep}, nil
	}, release)
	if !errors.Is(err, ErrOtherTelescope) || got != nil || dials != 0 || !r.Conflict || r.Seen == nil || r.NotSeen || r.Moved {
		t.Fatalf("got %v %+v %v, dials %d", got, r, err, dials)
	}
}

// Another serial answering from the saved address while it is dialed
// disqualifies it: the connection is released, and the search goes on in
// the same scan.
func TestFollowConflictDuringSavedDial(t *testing.T) {
	t.Run("nothing else answers", func(t *testing.T) {
		s, c := observedScan(time.Second, answer("SERIAL-A", "192.0.2.20"))
		var sessions []*session
		got, r, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
			c.deliver(t, answer("SERIAL-B", "192.0.2.20"))
			sessions = append(sessions, &session{endpoint: ep})
			return sessions[len(sessions)-1], nil
		}, release)
		if !errors.Is(err, ErrOtherTelescope) || got != nil || len(sessions) != 1 || !sessions[0].closed.Load() ||
			!r.Conflict || r.Seen == nil || r.NotSeen {
			t.Fatalf("got %v %+v %v, sessions %d", got, r, err, len(sessions))
		}
	})
	t.Run("the telescope answers elsewhere", func(t *testing.T) {
		s, c := observedScan(time.Minute, answer("SERIAL-A", "192.0.2.20"))
		sessions := map[string]*session{}
		got, r, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
			if ep == "192.0.2.20:445" {
				c.deliver(t, answer("SERIAL-B", "192.0.2.20"), answer("SERIAL-A", "192.0.2.30"))
			}
			sessions[ep] = &session{endpoint: ep}
			return sessions[ep], nil
		}, release)
		saved, other := sessions["192.0.2.20:445"], sessions["192.0.2.30:445"]
		if err != nil || got == nil || got != other || other.closed.Load() || saved == nil || !saved.closed.Load() ||
			!r.Moved || !r.Conflict || r.Endpoint != "192.0.2.30:445" || !c.isClosed() {
			t.Fatalf("got %v %+v %v, sessions %v", got, r, err, sessions)
		}
	})
}

// The saved address answering with the serial while another address is
// dialed is still preferred: it is dialed too, and the other connection is
// released if it connects.
func TestFollowSavedMatchDuringOtherDial(t *testing.T) {
	// dial connects everywhere but at down; dialing first, it delivers
	// during, and dialing second, the saved address, it delivers then.
	type script struct {
		during, then []datagram
		down         string
		cancel       bool // the saved address's dial is canceled
	}
	run := func(t *testing.T, sc script) (*session, Resolved, error, map[string]*session) {
		s, c := observedScan(time.Minute, answer("SERIAL-A", "192.0.2.30"))
		sessions := map[string]*session{}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		got, r, err := Follow(ctx, s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
			switch ep {
			case "192.0.2.30:445":
				c.deliver(t, sc.during...)
			case "192.0.2.20:445":
				if sc.then != nil {
					c.deliver(t, sc.then...)
				}
				if sc.cancel {
					cancel()
					return nil, ctx.Err()
				}
			}
			if ep == sc.down {
				return nil, errors.New("refused")
			}
			sessions[ep] = &session{endpoint: ep}
			return sessions[ep], nil
		}, release)
		return got, r, err, sessions
	}
	t.Run("the saved address connects", func(t *testing.T) {
		got, r, err, sessions := run(t, script{during: []datagram{answer("SERIAL-A", "192.0.2.20")}})
		saved, other := sessions["192.0.2.20:445"], sessions["192.0.2.30:445"]
		if err != nil || got == nil || got != saved || saved.closed.Load() || other == nil || !other.closed.Load() ||
			r.Moved || r.Endpoint != "192.0.2.20:445" {
			t.Fatalf("got %v %+v %v, sessions %v", got, r, err, sessions)
		}
	})
	t.Run("the saved address doesn't connect", func(t *testing.T) {
		got, r, err, sessions := run(t, script{during: []datagram{answer("SERIAL-A", "192.0.2.20")}, down: "192.0.2.20:445"})
		other := sessions["192.0.2.30:445"]
		if err != nil || got == nil || got != other || other.closed.Load() || !r.Moved || r.Endpoint != "192.0.2.30:445" {
			t.Fatalf("got %v %+v %v, sessions %v", got, r, err, sessions)
		}
	})
	t.Run("the saved address's dial is canceled", func(t *testing.T) {
		got, _, err, sessions := run(t, script{during: []datagram{answer("SERIAL-A", "192.0.2.20")}, cancel: true})
		other := sessions["192.0.2.30:445"]
		if !errors.Is(err, context.Canceled) || got != nil || other == nil || !other.closed.Load() {
			t.Fatalf("got %v %v, sessions %v", got, err, sessions)
		}
	})
	t.Run("another serial answers at the saved address during its dial", func(t *testing.T) {
		got, r, err, sessions := run(t, script{
			during: []datagram{answer("SERIAL-A", "192.0.2.20")},
			then:   []datagram{answer("SERIAL-B", "192.0.2.20")},
		})
		saved, other := sessions["192.0.2.20:445"], sessions["192.0.2.30:445"]
		if err != nil || got == nil || got != other || other.closed.Load() || saved == nil || !saved.closed.Load() ||
			!r.Moved || !r.Conflict || r.Endpoint != "192.0.2.30:445" {
			t.Fatalf("got %v %+v %v, sessions %v", got, r, err, sessions)
		}
	})
	t.Run("another serial answered at the saved address first", func(t *testing.T) {
		got, r, err, sessions := run(t, script{during: []datagram{answer("SERIAL-B", "192.0.2.20"), answer("SERIAL-A", "192.0.2.20")}})
		other := sessions["192.0.2.30:445"]
		if err != nil || got == nil || got != other || other.closed.Load() || sessions["192.0.2.20:445"] != nil ||
			!r.Moved || !r.Conflict {
			t.Fatalf("got %v %+v %v, sessions %v", got, r, err, sessions)
		}
	})
}
