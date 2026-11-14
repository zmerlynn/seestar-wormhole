package landiscovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
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
	got, r, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
		dials++
		return &session{endpoint: ep}, nil
	}, release)
	if !errors.Is(err, ErrOtherTelescope) || got != nil || dials != 0 || !r.Conflict || r.Seen == nil || r.NotSeen || r.Moved {
		t.Fatalf("got %v %+v %v, dials %d", got, r, err, dials)
	}
}

// otherAt reports whether err is an [OtherTelescopeError] for SERIAL-A
// naming the telescope with other that answered at address.
func otherAt(err error, address, other string) bool {
	e, ok := errors.AsType[*OtherTelescopeError](err)
	return ok && errors.Is(err, ErrOtherTelescope) && e.Serial == "SERIAL-A" && e.Address == netip.MustParseAddr(address) &&
		e.Other != nil && e.Other.Serial == other && e.Other.Addresses[0] == e.Address
}

// The owner's setup of 2026-11-14: the telescope saved with its serial at
// a relay's SMB port, where the scan hears only another serial. The
// error names both, for the pages and the logs.
func TestFollowOtherTelescopeNamed(t *testing.T) {
	s, _ := observedScan(100*time.Millisecond, datagram{replyBytes("TEST0002", "Relay", "Seestar S50 Pro", 0), netip.MustParseAddr("192.0.2.85")})
	dials := 0
	_, r, _, err := Follow(t.Context(), s, "192.0.2.85:4445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
		dials++
		return &session{endpoint: ep}, nil
	}, release)
	if !otherAt(err, "192.0.2.85", "TEST0002") || dials != 0 || !r.Conflict || r.Seen != nil {
		t.Fatalf("%+v %v, dials %d", r, err, dials)
	}
	if want := "another telescope, Relay (Seestar S50 Pro, serial TEST0002), answered at 192.0.2.85 instead of serial SERIAL-A"; err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
}

// A reply can carry any text, up to a datagram. The error caps the other
// telescope's name and model, never within an escape, keeps a valid
// serial in full, and names the other by its serial alone, capped, when
// that isn't a valid one, so it stays within 4096 bytes. The reply itself
// is kept as it came.
func TestFollowOtherTelescopeBounded(t *testing.T) {
	del, acute := strings.Repeat("\x7f", 3700), strings.Repeat("é", 1000)
	cut := func(escape string, n int) string { return strings.Repeat(escape, n) + "..." }
	for _, c := range []struct {
		name, sn, ssid, model, want string
	}{
		{"long name", "TEST0002", del, "S50 Pro", cut(`\x7f`, 15) + " (S50 Pro, serial TEST0002)"},
		{"long model", "TEST0002", "Relay", acute, "Relay (" + cut(`\u00e9`, 10) + ", serial TEST0002)"},
		{"escaped valid serial", `TEST\"0002`, del, "S50 Pro", cut(`\x7f`, 15) + ` (S50 Pro, serial TEST\"0002)`},
		{"long serial", del, "Relay", "S50 Pro", "serial " + cut(`\x7f`, 15)},
		{"invalid serial", "TEST 0002", "Relay", "S50 Pro", "serial TEST 0002"},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := replyBytes(c.sn, c.ssid, c.model, 0)
			if len(raw) > MaxDatagram {
				t.Fatalf("reply of %d bytes", len(raw))
			}
			s, _ := observedScan(100*time.Millisecond, datagram{raw, netip.MustParseAddr("192.0.2.85")})
			dials := 0
			_, _, _, err := Follow(t.Context(), s, "192.0.2.85:4445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
				dials++
				return &session{endpoint: ep}, nil
			}, release)
			e, ok := errors.AsType[*OtherTelescopeError](err)
			if !errors.Is(err, ErrOtherTelescope) || !ok || dials != 0 || e.Other == nil || e.Other.Name != strings.ReplaceAll(c.ssid, `\"`, `"`) ||
				e.Other.Model != c.model || e.Other.Serial != strings.ReplaceAll(c.sn, `\"`, `"`) {
				t.Fatalf("%#v, dials %d", err, dials)
			}
			if want := "another telescope, " + c.want + ", answered at 192.0.2.85 instead of serial SERIAL-A"; err.Error() != want || len(want) > 4096 {
				t.Errorf("error %q, want %q", err, want)
			}
		})
	}
	if got, want := (&OtherTelescopeError{Serial: del}).Error(), "too many other telescopes answered the scan to use any address for serial "+cut(`\x7f`, 15); got != want {
		t.Errorf("saved serial: %q, want %q", got, want)
	}
}

// Another serial answering from the saved address while it is dialed
// disqualifies it: the connection is released, and the search goes on in
// the same scan.
func TestFollowConflictDuringSavedDial(t *testing.T) {
	t.Run("nothing else answers", func(t *testing.T) {
		s, c := observedScan(time.Second, answer("SERIAL-A", "192.0.2.20"))
		var sessions []*session
		got, r, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
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
		got, r, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
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
	// dial connects everywhere but at down's ports; dialing first, it delivers
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
		got, r, _, err := Follow(ctx, s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
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
			if host, _, _ := net.SplitHostPort(ep); host == sc.down {
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
		got, r, err, sessions := run(t, script{during: []datagram{answer("SERIAL-A", "192.0.2.20")}, down: "192.0.2.20"})
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

// Another serial answering from an address the telescope answered from
// disqualifies that address too, not only the saved one: before its dial,
// between its ports, and after it connects, up to the scan's end.
func TestFollowConflictAtAnotherAddress(t *testing.T) {
	t.Run("before the dial", func(t *testing.T) {
		s, _ := observedScan(100*time.Millisecond, answer("SERIAL-B", "192.0.2.30"), answer("SERIAL-A", "192.0.2.30"))
		dials := 0
		got, r, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
			dials++
			return &session{endpoint: ep}, nil
		}, release)
		if !otherAt(err, "192.0.2.30", "SERIAL-B") || got != nil || dials != 0 || r.Conflict || r.Seen == nil || r.NotSeen {
			t.Fatalf("got %v %+v %v, dials %d", got, r, err, dials)
		}
	})
	t.Run("during the dial on 4445", func(t *testing.T) {
		s, c := observedScan(100*time.Millisecond, answer("SERIAL-A", "192.0.2.30"))
		var opened *session
		got, r, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
			if ep == "192.0.2.30:445" {
				return nil, errors.New("445 refused")
			}
			c.deliver(t, answer("SERIAL-B", "192.0.2.30"))
			opened = &session{endpoint: ep}
			return opened, nil
		}, release)
		if !otherAt(err, "192.0.2.30", "SERIAL-B") || got != nil || opened == nil || !opened.closed.Load() || r.Moved || r.Conflict {
			t.Fatalf("got %v %+v %v, opened %v", got, r, err, opened)
		}
	})
	t.Run("at the saved address during the dial on 4445", func(t *testing.T) {
		s, c := observedScan(100*time.Millisecond, answer("SERIAL-A", "192.0.2.20"))
		var opened *session
		got, r, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
			if ep == "192.0.2.20:445" {
				return nil, errors.New("445 refused")
			}
			c.deliver(t, answer("SERIAL-B", "192.0.2.20"))
			opened = &session{endpoint: ep}
			return opened, nil
		}, release)
		if !otherAt(err, "192.0.2.20", "SERIAL-B") || got != nil || opened == nil || !opened.closed.Load() || !r.Conflict {
			t.Fatalf("got %v %+v %v, opened %v", got, r, err, opened)
		}
	})
	t.Run("between the ports", func(t *testing.T) {
		s, c := observedScan(100*time.Millisecond, answer("SERIAL-A", "192.0.2.30"))
		var tried []string
		_, _, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
			tried = append(tried, ep)
			c.deliver(t, answer("SERIAL-B", "192.0.2.30"))
			return nil, errors.New("445 refused")
		}, release)
		if err == nil || len(tried) != 1 {
			t.Fatalf("%v tried %v", err, tried)
		}
	})
	t.Run("the search goes on elsewhere", func(t *testing.T) {
		s, c := observedScan(time.Minute, answer("SERIAL-A", "192.0.2.30"))
		sessions := map[string]*session{}
		got, r, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
			if ep == "192.0.2.30:445" {
				c.deliver(t, answer("SERIAL-B", "192.0.2.30"), answer("SERIAL-A", "192.0.2.31"))
			}
			sessions[ep] = &session{endpoint: ep}
			return sessions[ep], nil
		}, release)
		first, second := sessions["192.0.2.30:445"], sessions["192.0.2.31:445"]
		if err != nil || got == nil || got != second || second.closed.Load() || first == nil || !first.closed.Load() ||
			r.Endpoint != "192.0.2.31:445" || !r.Moved || r.Conflict {
			t.Fatalf("got %v %+v %v, sessions %v", got, r, err, sessions)
		}
	})
}

// Past maxOthers addresses answering with other serials, Follow can't tell
// which addresses are disqualified, and uses none: not where serial
// answered, nor the saved address when it answered nowhere.
func TestFollowFailsClosedWhenOthersFill(t *testing.T) {
	for _, seen := range []bool{true, false} {
		t.Run(fmt.Sprint("serial seen ", seen), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var replies []datagram
				for i := range maxOthers + 1 {
					replies = append(replies, datagram{replyBytes(fmt.Sprintf("OTHER-%d", i), "S50_X", "Seestar S50", 0), netip.AddrFrom4([4]byte{198, 51, byte(i >> 8), byte(i)})})
				}
				if seen {
					replies = append(replies, answer("SERIAL-A", "192.0.2.30"))
				}
				s, c := observedScan(time.Second, replies...)
				c.in = make(chan datagram, len(replies)) // more than it usually buffers
				dials := 0
				got, r, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
					dials++
					return &session{endpoint: ep}, nil
				}, release)
				if !errors.Is(err, ErrOtherTelescope) || got != nil || dials != 0 || r.Conflict || r.NotSeen || r.Replies != len(replies) {
					t.Fatalf("got %v %+v %v, dials %d", got, r, err, dials)
				}
				// None of the addresses remembered is one it would have used.
				if e, ok := errors.AsType[*OtherTelescopeError](err); !ok || e.Other != nil || e.Address.IsValid() ||
					err.Error() != "too many other telescopes answered the scan to use any address for serial SERIAL-A" {
					t.Errorf("error %#v", err)
				}
			})
		})
	}
}

// bothRefused reports whether err is a refusal on 445 then 4445 at host,
// naming both.
func bothRefused(err error, host string) bool {
	pe, ok := errors.AsType[*PortsError](err)
	return ok && errors.Is(err, syscall.ECONNREFUSED) && pe.Endpoint() == host+":445" &&
		strings.Contains(err.Error(), host+":445: ") && strings.Contains(err.Error(), "; "+host+":4445: ")
}

// A failure on both ports names each, and is classified as the one that
// got further: the second when the first didn't connect and the second
// did, or when the second was canceled; otherwise the first.
func TestPortsError(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	timeout := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	denied := fmt.Errorf("mount: %w", os.ErrPermission)
	for _, c := range []struct {
		name   string
		errs   [2]error
		second bool
	}{
		{"both refused", [2]error{refused, refused}, false},
		{"refused, then the share denied", [2]error{refused, denied}, true},
		{"the share denied, then refused", [2]error{denied, refused}, false},
		{"no answer, then refused", [2]error{timeout, refused}, false},
		{"refused, then canceled", [2]error{refused, fmt.Errorf("dial: %w", context.Canceled)}, true},
		{"refused, then out of time", [2]error{refused, context.DeadlineExceeded}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := &PortsError{[2]string{"192.0.2.20:445", "192.0.2.20:4445"}, c.errs}
			want, endpoint := c.errs[0], "192.0.2.20:445"
			if c.second {
				want, endpoint = c.errs[1], "192.0.2.20:4445"
			}
			if !errors.Is(e, want) || e.Endpoint() != endpoint || errors.Unwrap(e) != want {
				t.Errorf("unwraps to %v at %s", errors.Unwrap(e), e.Endpoint())
			}
			if got := e.Error(); got != "192.0.2.20:445: "+c.errs[0].Error()+"; 192.0.2.20:4445: "+c.errs[1].Error() {
				t.Errorf("%q", got)
			}
		})
	}
}

// Follow's failure on both ports keeps the second's failure when it got
// further, and its cancellation.
func TestFollowKeepsTheFurtherFailure(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	t.Run("the proxy's share", func(t *testing.T) {
		s, _ := observedScan(100*time.Millisecond, answer("SERIAL-A", "192.0.2.20"))
		denied := errors.New("proxy share permission denied")
		_, _, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(_ context.Context, ep string) (*session, error) {
			if ep == "192.0.2.20:445" {
				return nil, refused
			}
			return nil, denied
		}, release)
		if !errors.Is(err, denied) || errors.Is(err, syscall.ECONNREFUSED) || !strings.Contains(err.Error(), "192.0.2.20:445: ") {
			t.Fatal(err)
		}
	})
	t.Run("canceled on 4445", func(t *testing.T) {
		s, _ := observedScan(time.Minute, answer("SERIAL-A", "192.0.2.20"))
		ctx, cancel := context.WithCancel(t.Context())
		_, _, _, err := Follow(ctx, s, "192.0.2.20:445", "SERIAL-A", func(ctx context.Context, ep string) (*session, error) {
			if ep == "192.0.2.20:445" {
				return nil, refused
			}
			cancel()
			<-ctx.Done()
			return nil, fmt.Errorf("dial %s: %w", ep, ctx.Err())
		}, release)
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

// One ConnectTimeout spans the scan and both ports: the second port gets
// only what the first left, and the connect fails as a timeout. A
// connection made within it outlives it.
func TestFollowConnectTimeout(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	t.Run("spans both ports", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, _ := observedScan(time.Hour, answer("SERIAL-A", "192.0.2.20"))
			start := time.Now()
			var second time.Duration
			_, r, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(ctx context.Context, ep string) (*session, error) {
				if ep == "192.0.2.20:445" {
					time.Sleep(20 * time.Second)
					return nil, refused
				}
				began := time.Now()
				<-ctx.Done()
				second = time.Since(began)
				return nil, fmt.Errorf("dial %s: %w", ep, ctx.Err())
			}, release)
			if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || time.Since(start) != ConnectTimeout ||
				second != ConnectTimeout-20*time.Second || r.Moved || r.Endpoint != "192.0.2.20:445" {
				t.Fatalf("%+v %v after %v, second port %v", r, err, time.Since(start), second)
			}
		})
	})
	t.Run("a connection outlives it", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, _ := observedScan(time.Hour, answer("SERIAL-A", "192.0.2.20"))
			var bound context.Context
			got, _, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(ctx context.Context, ep string) (*session, error) {
				time.Sleep(ConnectTimeout - time.Second)
				bound = ctx
				return &session{endpoint: ep}, nil
			}, release)
			if err != nil || got == nil {
				t.Fatal(err)
			}
			time.Sleep(time.Hour)
			if bound.Err() != nil || got.closed.Load() {
				t.Fatalf("the connection's context ended: %v", bound.Err())
			}
		})
	})
	t.Run("a connection as it runs out is released", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, _ := observedScan(time.Hour, answer("SERIAL-A", "192.0.2.20"))
			var opened *session
			got, r, _, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", func(ctx context.Context, ep string) (*session, error) {
				time.Sleep(ConnectTimeout + time.Second)
				opened = &session{endpoint: ep}
				return opened, nil
			}, release)
			if !errors.Is(err, context.DeadlineExceeded) || got != nil || !opened.closed.Load() || r.Endpoint != "192.0.2.20:445" {
				t.Fatalf("%v %+v %v", got, r, err)
			}
		})
	})
	t.Run("the caller's cancellation stays one", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, _ := observedScan(time.Hour, answer("SERIAL-A", "192.0.2.20"))
			ctx, cancel := context.WithCancel(t.Context())
			_, _, _, err := Follow(ctx, s, "192.0.2.20:445", "SERIAL-A", func(ctx context.Context, ep string) (*session, error) {
				time.AfterFunc(time.Second, cancel)
				<-ctx.Done()
				return nil, ctx.Err()
			}, release)
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	})
}

// trackedParent counts the contexts waiting on it to end. context
// registers a child through a parent's AfterFunc method when the parent
// shows it no cancelCtx of its own, as Value's nil does.
type trackedParent struct {
	context.Context
	waiting atomic.Int32
}

func (p *trackedParent) Value(any) any { return nil }

func (p *trackedParent) AfterFunc(f func()) func() bool {
	p.waiting.Add(1)
	stop := context.AfterFunc(p.Context, func() { p.waiting.Add(-1); f() })
	return func() bool {
		if stop() {
			p.waiting.Add(-1)
			return true
		}
		return false
	}
}

// A connection Follow returns keeps its context past ConnectTimeout, and
// the cancel returned with it ends that context and leaves nothing
// waiting on ctx.
func TestFollowReturnsItsCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent := &trackedParent{Context: t.Context()}
		for range 3 {
			s, _ := observedScan(time.Hour, answer("SERIAL-A", "192.0.2.20"))
			var bound context.Context
			got, _, cancel, err := Follow(parent, s, "192.0.2.20:445", "SERIAL-A", func(ctx context.Context, ep string) (*session, error) {
				bound = ctx
				return &session{endpoint: ep}, nil
			}, release)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(ConnectTimeout + time.Second)
			if bound.Err() != nil || parent.waiting.Load() != 1 {
				t.Fatalf("open: context %v, %d waiting", bound.Err(), parent.waiting.Load())
			}
			release(got)
			cancel()
			if bound.Err() == nil || parent.waiting.Load() != 0 {
				t.Fatalf("closed: context %v, %d waiting", bound.Err(), parent.waiting.Load())
			}
		}
	})
}
