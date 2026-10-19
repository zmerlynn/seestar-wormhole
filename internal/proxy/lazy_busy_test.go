package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

type lazyBusyCloud struct {
	simulatedCloud
	route     string
	ready     time.Time
	unblocked atomic.Bool
	attempts  atomic.Int32
}

func (c *lazyBusyCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	path := "/v1/my-device/detail"
	if c.route == "allocation" {
		path = "/v1/device-p2p/online-link"
	}
	if r.URL.Path != path {
		return c.simulatedCloud.RoundTrip(r)
	}
	c.attempts.Add(1)
	if c.unblocked.Load() || (!c.ready.IsZero() && !time.Now().Before(c.ready)) {
		return c.simulatedCloud.RoundTrip(r)
	}
	status := 200
	body := `{"code":200,"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":4,"ssid":"synthetic-wifi"}}`
	switch c.route {
	case "allocation":
		status = 400
		body = `{"code":300404,"data":null}`
	case "auth":
		status = 401
		body = `{"code":401,"data":null}`
	case "serial":
		body = `{"code":200,"data":{"deviceSn":"","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`
	case "protocol":
		body = `{"code":200,"data":false}`
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func lazyBusySession(t *testing.T, ctx context.Context, cfg Config, tr *lazyBusyCloud) *Session {
	t.Helper()
	c, err := newFixtureClient(t, cloudhttp.Config{Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	var trace []string
	s, err := startWithRNG(ctx, cfg, func(s *Session) sessionedge.Factory {
		return &factory{s: s, cloud: c, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner { return newSimulatedWire(t, &trace) }}
	}, zeroRetryRNG{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLazyBusyThenReady(t *testing.T) {
	for _, route := range []string{"precheck", "allocation"} {
		for _, window := range []time.Duration{36 * time.Second, 81 * time.Second} {
			t.Run(route+"/"+window.String(), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					tr := &lazyBusyCloud{route: route, ready: time.Now().Add(window)}
					var logs bytes.Buffer
					s := lazyBusySession(t, context.Background(), Config{Idle: time.Minute, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, tr)
					defer s.Close()
					began := time.Now()
					conn, err := s.DialPort(context.Background(), 4700)
					if err != nil {
						t.Fatalf("busy-then-ready client failed: %v", err)
					}
					defer conn.Close()
					if elapsed := time.Since(began); elapsed < window || elapsed > 95*time.Second {
						t.Fatalf("ready after %v, want %v..95s", elapsed, window)
					}
					if tr.attempts.Load() < 3 || tr.allocs.Load() != 1 {
						t.Fatalf("attempts/allocations=%d/%d", tr.attempts.Load(), tr.allocs.Load())
					}
					conn.SetDeadline(time.Now().Add(5 * time.Second))
					conn.Write([]byte(rpcLine(1, 0)))
					b := make([]byte, 256)
					n, err := conn.Read(b)
					if err != nil || checkResponse(string(b[:n]), 1) != nil {
						t.Fatalf("RPC failed: %q %v", b[:n], err)
					}
					if !strings.Contains(logs.String(), `"msg":"lazy_open_attempt"`) || !strings.Contains(logs.String(), `"cause":"busy"`) {
						t.Fatalf("missing attempt cause logs: %s", logs.String())
					}
				})
			})
		}
	}
}

func TestLazyBusyBudgetAndFreshRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &lazyBusyCloud{route: "precheck"}
		s := lazyBusySession(t, context.Background(), Config{Idle: time.Minute}, tr)
		defer s.Close()
		began := time.Now()
		clients := [2]chan error{make(chan error, 1), make(chan error, 1)}
		open := func(done chan<- error) {
			_, err := s.DialPort(context.Background(), 4700)
			done <- err
		}
		go open(clients[0])
		synctest.Wait()
		<-time.After(89 * time.Second)
		go open(clients[1])
		synctest.Wait()
		<-time.After(time.Second)
		synctest.Wait()
		for i, done := range clients {
			select {
			case err := <-done:
				if !errors.Is(err, session.ErrBusy) {
					t.Fatalf("client %d exhaustion=%v, want busy", i, err)
				}
			default:
				t.Fatalf("client %d still waiting at original deadline", i)
			}
		}
		if elapsed := time.Since(began); elapsed != 90*time.Second {
			t.Fatalf("budget elapsed=%v, want 90s", elapsed)
		}
		synctest.Wait()
		select {
		case <-s.Done():
			t.Fatal("proxy exited on budget exhaustion")
		default:
		}
		attempts := tr.attempts.Load()
		time.Sleep(time.Minute)
		synctest.Wait()
		if tr.attempts.Load() != attempts {
			t.Fatal("retried without demand")
		}
		// Another busy request must get an independent budget too.
		began = time.Now()
		if _, err := s.DialPort(context.Background(), 4700); !errors.Is(err, session.ErrBusy) {
			t.Fatal(err)
		}
		if time.Since(began) != 90*time.Second || tr.attempts.Load() <= attempts {
			t.Fatal("next request did not start a fresh retry budget")
		}
		tr.unblocked.Store(true)
		conn, err := s.DialPort(context.Background(), 4700)
		if err != nil {
			t.Fatalf("request after exhausted budgets: %v", err)
		}
		conn.Close()
		if err = s.Close(); err != nil {
			t.Fatalf("shutdown after handled busy=%v", err)
		}
	})
}

func TestLazyNonBusyDoesNotRetry(t *testing.T) {
	for _, route := range []string{"auth", "serial", "protocol"} {
		t.Run(route, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tr := &lazyBusyCloud{route: route}
				s := lazyBusySession(t, context.Background(), Config{Idle: time.Minute}, tr)
				defer s.Close()
				if _, err := s.DialPort(context.Background(), 4700); err == nil {
					t.Fatal("invalid open succeeded")
				}
				time.Sleep(2 * time.Minute)
				synctest.Wait()
				if tr.attempts.Load() != 1 {
					t.Fatalf("non-busy attempts=%d, want 1", tr.attempts.Load())
				}
				if err := s.Close(); err == nil {
					t.Fatal("real failure lost at shutdown")
				}
			})
		})
	}
}

func TestLazyBusyCancelBackoff(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "client", true: "shutdown"}[shutdown], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tr := &lazyBusyCloud{route: "precheck"}
				s := lazyBusySession(t, context.Background(), Config{Idle: time.Minute}, tr)
				defer s.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := s.DialPort(ctx, 4700); done <- err }()
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("busy rejected before cancellation: %v", err)
				default:
				}
				attempts := tr.attempts.Load()
				began := time.Now()
				if shutdown {
					if err := s.Close(); err != nil {
						t.Fatalf("orderly shutdown=%v", err)
					}
				} else {
					cancel()
				}
				if err := <-done; err == nil {
					t.Fatal("canceled client succeeded")
				}
				synctest.Wait()
				if time.Since(began) != 0 {
					t.Fatal("cancellation waited for retry timer")
				}
				time.Sleep(2 * time.Minute)
				synctest.Wait()
				if tr.attempts.Load() != attempts {
					t.Fatal("canceled demand kept retrying")
				}
				if !shutdown {
					tr.unblocked.Store(true)
					conn, err := s.DialPort(context.Background(), 4700)
					if err != nil {
						t.Fatalf("fresh demand after cancellation: %v", err)
					}
					conn.Close()
				}
			})
		})
	}
}

func TestLazyBusyShutdownOutcome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &lazyBusyCloud{route: "precheck"}
		s := lazyBusySession(t, context.Background(), Config{Idle: time.Minute}, tr)
		defer s.Close()
		ctx, cancel := context.WithCancel(context.Background())
		go s.DialPort(ctx, 4700)
		synctest.Wait()
		cancel()
		synctest.Wait()
		if err := s.Close(); err != nil {
			t.Fatalf("no lease held; orderly cancellation returned %v", err)
		}
	})
}

func TestLazyBusyListenerKeepsServing(t *testing.T) {
	for _, exhaust := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "exhausted"}[exhaust], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				tr := &lazyBusyCloud{route: "precheck"}
				if !exhaust {
					tr.ready = time.Now().Add(36 * time.Second)
				}
				s := lazyBusySession(t, ctx, Config{Idle: time.Minute}, tr)
				defer s.Close()
				l := &admissionQueueListener{conns: make(chan net.Conn, 1), done: make(chan struct{})}
				done := make(chan error, 1)
				go func() { done <- Serve(ctx, s, []Forward{{Listener: l, Port: 4700}}, 1, nil) }()
				defer func() {
					cancel()
					if err := <-done; err != nil {
						t.Error(err)
					}
				}()
				clientRequest := func() net.Conn {
					local, client := net.Pipe()
					l.conns <- local
					client.SetDeadline(time.Now().Add(95 * time.Second))
					if _, err := client.Write([]byte(rpcLine(1, 0))); err != nil {
						t.Fatal(err)
					}
					return client
				}
				client := clientRequest()
				if exhaust {
					var b [1]byte
					if n, err := client.Read(b[:]); n != 0 || err != io.EOF {
						t.Fatalf("exhausted client=%d/%v want EOF", n, err)
					}
					client.Close()
					synctest.Wait()
					select {
					case err := <-done:
						t.Fatalf("Serve exited on busy: %v", err)
					default:
					}
					tr.unblocked.Store(true)
					client = clientRequest()
				}
				b := make([]byte, 256)
				n, err := client.Read(b)
				client.Close()
				if err != nil || checkResponse(string(b[:n]), 1) != nil {
					t.Fatalf("held listener client=%q/%v", b[:n], err)
				}
			})
		})
	}
}

func TestLazyBusyListenerClientGone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		tr := &lazyBusyCloud{route: "precheck"}
		s := lazyBusySession(t, ctx, Config{Idle: time.Minute}, tr)
		defer s.Close()
		local, client := net.Pipe()
		l := &e2eSingleListener{conn: lazyBusyResetConn{local}, done: make(chan struct{})}
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, s, []Forward{{Listener: l, Port: 4700}}, 1, nil) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		synctest.Wait()
		if tr.attempts.Load() != 1 {
			t.Fatalf("initial attempts=%d want 1", tr.attempts.Load())
		}
		client.Close()
		synctest.Wait()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if tr.attempts.Load() != 1 {
			t.Fatal("disconnected listener client kept retrying")
		}
	})
}

// net.Pipe has no half-close. Model its peer disappearing as a terminal socket
// error so this fixture does not conflate a TCP reset with a valid TCP FIN.
type lazyBusyResetConn struct{ net.Conn }

func (c lazyBusyResetConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err == io.EOF {
		err = net.ErrClosed
	}
	return n, err
}
