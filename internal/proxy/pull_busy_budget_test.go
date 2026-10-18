package proxy

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// Only the allocator and protocol adapters are fake. Admission, retry timers,
// cancellation, release, and rclone's SMB connection retry loop are production.
type pullBusyFactory struct {
	s         *Session
	allocate  func(context.Context) (sessionedge.Lease, error)
	channels  chan *pullBusyChannels
	stops     atomic.Int32
	closed    atomic.Int32
	stopError error
}

func (f *pullBusyFactory) New(uint64, session.Budgets) (sessionedge.Layers, error) {
	a := &pullBusyChannels{f: f, events: make(chan session.Event, 1)}
	f.channels <- a
	return sessionedge.Layers{Cloud: f, Rendezvous: a, DTLS: a, RDT: a, Tunnel: a}, nil
}
func (f *pullBusyFactory) Allocate(ctx context.Context) (sessionedge.Lease, error) {
	return f.allocate(ctx)
}
func (f *pullBusyFactory) Stop(context.Context) error {
	f.stops.Add(1)
	return f.stopError
}

type pullBusyTransport struct{ f *pullBusyFactory }

func (a *pullBusyTransport) Close() error               { a.f.closed.Add(1); return nil }
func (*pullBusyTransport) Events() <-chan session.Event { return nil }

type pullBusyChannels struct {
	f      *pullBusyFactory
	events chan session.Event
}

func (a *pullBusyChannels) Dial(context.Context, sessionedge.Lease) (sessionedge.Transport, error) {
	return &pullBusyTransport{a.f}, nil
}
func (a *pullBusyChannels) Handshake(context.Context, sessionedge.Transport) (sessionedge.Transport, error) {
	return &pullBusyTransport{a.f}, nil
}
func (a *pullBusyChannels) Create(context.Context, sessionedge.Transport) (sessionedge.Transport, error) {
	return &pullBusyTransport{a.f}, nil
}
func (a *pullBusyChannels) Start(context.Context, sessionedge.Transport) (sessionedge.Channels, error) {
	return a, nil
}
func (a *pullBusyChannels) Events() <-chan session.Event { return a.events }
func (a *pullBusyChannels) Close() error                 { a.f.closed.Add(1); return nil }
func (*pullBusyChannels) TryClose(uint64)                {}
func (a *pullBusyChannels) TryOpen(id uint64) error {
	a.f.s.mu.Lock()
	defer a.f.s.mu.Unlock()
	a.f.s.clients[id].result <- nil
	return nil
}

func pullBusySession(t *testing.T, ctx context.Context, budget time.Duration, f *pullBusyFactory) *Session {
	t.Helper()
	f.channels = make(chan *pullBusyChannels, 128)
	s, err := startWithRNG(ctx, Config{HoldOnDemand: true, BusyWait: budget}, func(s *Session) sessionedge.Factory {
		f.s = s
		return f
	}, zeroRetryRNG{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		// rclone leaves one bounded pacer delay after its last retry.
		<-time.After(time.Second)
		synctest.Wait()
	})
	return s
}

func TestPullBusySMBBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget time.Duration
		ready  time.Duration
	}{
		{"fail-fast", 0, 0},
		{"expiry", 90 * time.Second, 0},
		{"available-after-expiry", 90 * time.Second, 91 * time.Second},
		{"late-availability", 90 * time.Second, 36 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				began := time.Now()
				var attempts, admitted, dials int
				f := &pullBusyFactory{}
				f.allocate = func(context.Context) (sessionedge.Lease, error) {
					attempts++
					if tc.budget > 0 && time.Since(began) >= tc.budget {
						t.Errorf("allocation after budget: %v", time.Since(began))
					}
					if tc.ready > 0 && time.Since(began) >= tc.ready {
						return f, nil
					}
					return nil, session.ErrBusy
				}
				s := pullBusySession(t, context.Background(), tc.budget, f)
				ctx := pull.Configure(context.Background(), pull.DefaultConfig())
				smbFailure := errors.New("injected SMB connection failure after admission")
				_, _, err := pull.OpenSMB(ctx, func(ctx context.Context, _, _ string) (net.Conn, error) {
					dials++
					c, err := s.DialPort(ctx, 445)
					if err != nil {
						return nil, err
					}
					admitted++
					c.Close()
					return nil, smbFailure
				})
				synctest.Wait()
				if dials != 3 {
					t.Fatalf("rclone connection attempts=%d, want 3", dials)
				}
				if tc.ready == 36*time.Second {
					if !errors.Is(err, smbFailure) || admitted != 3 || time.Since(began) >= tc.budget {
						t.Fatalf("late admission: err=%v admitted=%d elapsed=%v", err, admitted, time.Since(began))
					}
				} else {
					if !errors.Is(err, session.ErrBusy) || admitted != 0 || time.Since(began) < tc.budget || time.Since(began) > tc.budget+time.Second {
						t.Fatalf("busy: err=%v admitted=%d elapsed=%v, want budget=%v", err, admitted, time.Since(began), tc.budget)
					}
					if tc.budget == 0 && attempts != 1 {
						t.Fatalf("fail-fast allocation attempts=%d, want 1", attempts)
					}
				}
				if err := s.Close(); err != nil && !(tc.budget == 0 && errors.Is(err, session.ErrBusy)) {
					t.Fatalf("clean release: %v", err)
				}
				wantStops := int32(0)
				if admitted > 0 {
					wantStops = 1
				}
				if f.stops.Load() != wantStops || f.closed.Load() != 4*wantStops {
					t.Fatalf("stops=%d closes=%d, want %d/%d", f.stops.Load(), f.closed.Load(), wantStops, 4*wantStops)
				}
				t.Logf("elapsed=%v allocations=%d backend dials=%d admissions=%d", time.Since(began), attempts, dials, admitted)
			})
		})
	}
}

func TestPullBusyTransportRetriesShareBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		began := time.Now()
		attempts := 0
		f := &pullBusyFactory{}
		f.allocate = func(ctx context.Context) (sessionedge.Lease, error) {
			attempts++
			if time.Since(began) >= 90*time.Second {
				t.Errorf("late allocation at %v", time.Since(began))
			}
			if attempts <= 2 {
				select {
				case <-time.After(20 * time.Second):
					return nil, session.ErrTransport
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, session.ErrBusy
		}
		s := pullBusySession(t, context.Background(), 90*time.Second, f)
		ctx := pull.Configure(context.Background(), pull.DefaultConfig())
		_, _, err := pull.OpenSMB(ctx, func(ctx context.Context, _, _ string) (net.Conn, error) { return s.DialPort(ctx, 445) })
		if !errors.Is(err, session.ErrBusy) || time.Since(began) < 90*time.Second || time.Since(began) > 91*time.Second {
			t.Fatalf("repeated transport retries: err=%v elapsed=%v attempts=%d", err, time.Since(began), attempts)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

type pullDelayedLease struct{ *pullBusyFactory }

func (f pullDelayedLease) Stop(ctx context.Context) error {
	select {
	case <-time.After(time.Second):
		return f.pullBusyFactory.Stop(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A partial allocation keeps cleanup in flight longer than rclone's pacing.
// All three backend retries must reach the owner without renewing its budget.
func TestPullBusyTransportRetryDuringCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		began := time.Now()
		attempts, dials := 0, 0
		f := &pullBusyFactory{}
		f.allocate = func(ctx context.Context) (sessionedge.Lease, error) {
			attempts++
			if time.Since(began) >= 90*time.Second {
				t.Errorf("late allocation at %v", time.Since(began))
			}
			if attempts <= 2 {
				select {
				case <-time.After(20 * time.Second):
					return pullDelayedLease{f}, session.ErrTransport
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, session.ErrBusy
		}
		s := pullBusySession(t, context.Background(), 90*time.Second, f)
		_, _, err := pull.OpenSMB(pull.Configure(context.Background(), pull.DefaultConfig()), func(ctx context.Context, _, _ string) (net.Conn, error) {
			dials++
			return s.DialPort(ctx, 445)
		})
		if !errors.Is(err, session.ErrBusy) || time.Since(began) < 90*time.Second || time.Since(began) > 91*time.Second || dials != 3 {
			t.Fatalf("retry during cleanup: err=%v elapsed=%v attempts=%d dials=%d, want busy at 90s across three dials", err, time.Since(began), attempts, dials)
		}
		if err := s.Close(); err != nil || f.stops.Load() != 2 {
			t.Fatalf("cleanup: err=%v stops=%d, want nil/2", err, f.stops.Load())
		}
	})
}

func TestPullBusyCancellationAndCleanup(t *testing.T) {
	for _, stopErr := range []error{nil, session.ErrStop} {
		t.Run(map[bool]string{true: "failed-release", false: "clean-release"}[stopErr != nil], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				f := &pullBusyFactory{stopError: stopErr}
				f.allocate = func(context.Context) (sessionedge.Lease, error) { return f, session.ErrBusy }
				s := pullBusySession(t, ctx, 90*time.Second, f)
				go func() { <-time.After(time.Second); cancel() }()
				began := time.Now()
				_, _, err := pull.OpenSMB(pull.Configure(ctx, pull.DefaultConfig()), func(ctx context.Context, _, _ string) (net.Conn, error) { return s.DialPort(ctx, 445) })
				if stopErr == nil && (!errors.Is(err, context.Canceled) || time.Since(began) > 2*time.Second) {
					t.Fatalf("canceled wait: %v elapsed=%v", err, time.Since(began))
				}
				closeErr := s.Close()
				if !errors.Is(closeErr, stopErr) || f.stops.Load() != 1 {
					t.Fatalf("release=%v stops=%d, want %v/1", closeErr, f.stops.Load(), stopErr)
				}
			})
		})
	}
}

func TestPullBusyInFlightLease(t *testing.T) {
	for _, cancelAt := range []time.Duration{time.Second, 0} {
		t.Run(cancelAt.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				f := &pullBusyFactory{}
				attempts := 0
				f.allocate = func(ctx context.Context) (sessionedge.Lease, error) {
					attempts++
					<-ctx.Done()
					return f, ctx.Err() // The canceled allocator still hands back ownership.
				}
				s := pullBusySession(t, ctx, 90*time.Second, f)
				if cancelAt > 0 {
					go func() { <-time.After(cancelAt); cancel() }()
				}
				began := time.Now()
				_, _, err := pull.OpenSMB(pull.Configure(ctx, pull.DefaultConfig()), func(ctx context.Context, _, _ string) (net.Conn, error) { return s.DialPort(ctx, 445) })
				want, limit := session.ErrBusy, 91*time.Second
				if cancelAt > 0 {
					want, limit = context.Canceled, 2*time.Second
				}
				if !errors.Is(err, want) || time.Since(began) > limit {
					t.Fatalf("in-flight lease: err=%v elapsed=%v, want %v by %v", err, time.Since(began), want, limit)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if attempts != 1 || f.stops.Load() != 1 {
					t.Fatalf("attempts=%d stops=%d, want 1/1", attempts, f.stops.Load())
				}
			})
		})
	}
}

func TestPullBusyFailFastAfterOwnRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		attempts := 0
		f := &pullBusyFactory{}
		f.allocate = func(context.Context) (sessionedge.Lease, error) {
			attempts++
			if attempts == 1 {
				return f, nil
			}
			return nil, session.ErrBusy
		}
		s := pullBusySession(t, context.Background(), 0, f)
		c, err := s.DialPort(context.Background(), 445)
		if err != nil {
			t.Fatal(err)
		}
		(<-f.channels).events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
		synctest.Wait()
		c.Close()
		began := time.Now()
		_, _, err = pull.OpenSMB(pull.Configure(context.Background(), pull.DefaultConfig()), func(ctx context.Context, _, _ string) (net.Conn, error) { return s.DialPort(ctx, 445) })
		if !errors.Is(err, session.ErrBusy) || attempts != 2 || time.Since(began) > time.Second {
			t.Fatalf("fail-fast recovery: err=%v attempts=%d elapsed=%v", err, attempts, time.Since(began))
		}
		if err := s.Close(); !errors.Is(err, session.ErrBusy) {
			t.Fatal(err)
		}
		if f.stops.Load() != 1 || f.closed.Load() != 4 {
			t.Fatalf("stops=%d closes=%d", f.stops.Load(), f.closed.Load())
		}
	})
}

func TestPullBusyReacquisition(t *testing.T) {
	for _, lostAt := range []time.Duration{20 * time.Second, 89 * time.Second, 89500 * time.Millisecond, 91 * time.Second, 10 * time.Minute} {
		t.Run(lostAt.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				began := time.Now()
				var attempts int
				f := &pullBusyFactory{}
				f.allocate = func(context.Context) (sessionedge.Lease, error) {
					attempts++
					if attempts == 1 || time.Since(began) >= 30*time.Second {
						return f, nil
					}
					return nil, session.ErrBusy
				}
				s := pullBusySession(t, context.Background(), 90*time.Second, f)
				c, err := s.DialPort(context.Background(), 445)
				if err != nil {
					t.Fatal(err)
				}
				a := <-f.channels
				<-time.After(lostAt)
				// A healthy acquired session must survive beyond the budget.
				if _, err := s.DialPort(context.Background(), 445); err != nil {
					t.Fatal(err)
				}
				a.events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
				synctest.Wait()
				c.Close()
				ctx := pull.Configure(context.Background(), pull.DefaultConfig())
				admitted := 0
				_, _, err = pull.OpenSMB(ctx, func(ctx context.Context, _, _ string) (net.Conn, error) {
					c, err := s.DialPort(ctx, 445)
					if err != nil {
						return nil, err
					}
					admitted++
					c.Close()
					return nil, net.ErrClosed
				})
				wantStops := int32(2)
				if admitted != 3 || !errors.Is(err, net.ErrClosed) {
					t.Fatalf("transport recovery: admissions=%d err=%v", admitted, err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if f.stops.Load() != wantStops || f.closed.Load() != 4*wantStops {
					t.Fatalf("recovery stops=%d closes=%d, want %d/%d", f.stops.Load(), f.closed.Load(), wantStops, 4*wantStops)
				}
			})
		})
	}
}

func TestPullBusyRecoveryDoesNotRenewBudget(t *testing.T) {
	for _, lostAt := range []time.Duration{20 * time.Second, 89500 * time.Millisecond, 91 * time.Second, 10 * time.Minute} {
		t.Run(lostAt.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				began := time.Now()
				attempts := 0
				f := &pullBusyFactory{}
				f.allocate = func(context.Context) (sessionedge.Lease, error) {
					attempts++
					if attempts == 1 {
						return f, nil
					}
					return nil, session.ErrBusy
				}
				s := pullBusySession(t, context.Background(), 90*time.Second, f)
				c, err := s.DialPort(context.Background(), 445)
				if err != nil {
					t.Fatal(err)
				}
				channels := <-f.channels
				<-time.After(lostAt)
				channels.events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
				synctest.Wait()
				c.Close()
				_, _, err = pull.OpenSMB(pull.Configure(context.Background(), pull.DefaultConfig()), func(ctx context.Context, _, _ string) (net.Conn, error) {
					return s.DialPort(ctx, 445)
				})
				// Recovery can allocate after expiry, but occupancy only gets the
				// remainder of the original budget, including all backend retries.
				wantEnd := max(90*time.Second, lostAt+500*time.Millisecond)
				if !errors.Is(err, session.ErrBusy) || time.Since(began) < wantEnd || time.Since(began) > wantEnd+time.Second || attempts < 2 {
					t.Fatalf("recovery busy: err=%v attempts=%d elapsed=%v, want busy by %v", err, attempts, time.Since(began), wantEnd+time.Second)
				}
				if lostAt >= 89500*time.Millisecond && attempts != 2 {
					t.Errorf("spent-budget recovery allocations=%d, want 2", attempts)
				}
				if closeErr := s.Close(); closeErr != nil && !errors.Is(closeErr, session.ErrBusy) {
					t.Fatal(closeErr)
				}
				if f.stops.Load() != 1 || f.closed.Load() != 4 {
					t.Fatalf("stops=%d closes=%d, want 1/4", f.stops.Load(), f.closed.Load())
				}
			})
		})
	}
}

func TestPullTransportRecoveryRetriesAfterBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()
		attempts := 0
		f := &pullBusyFactory{}
		f.allocate = func(context.Context) (sessionedge.Lease, error) {
			attempts++
			if attempts == 2 {
				return nil, session.ErrTransport
			}
			return f, nil
		}
		s := pullBusySession(t, ctx, 90*time.Second, f)
		c, err := s.DialPort(ctx, 445)
		if err != nil {
			t.Fatal(err)
		}
		channels := <-f.channels
		<-time.After(10 * time.Minute)
		channels.events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
		synctest.Wait()
		c.Close()
		began := time.Now()
		admissions := 0
		_, _, err = pull.OpenSMB(pull.Configure(ctx, pull.DefaultConfig()), func(ctx context.Context, _, _ string) (net.Conn, error) {
			c, err := s.DialPort(ctx, 445)
			if err != nil {
				return nil, err
			}
			admissions++
			c.Close()
			return nil, net.ErrClosed
		})
		if attempts != 3 || admissions != 2 || !errors.Is(err, net.ErrClosed) || time.Since(began) < 1500*time.Millisecond || time.Since(began) > 1600*time.Millisecond {
			t.Errorf("transport retries: attempts=%d admissions=%d err=%v elapsed=%v, want 3/2/closed at 1.5s", attempts, admissions, err, time.Since(began))
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if f.stops.Load() != 2 || f.closed.Load() != 8 {
			t.Fatalf("stops=%d closes=%d, want 2/8", f.stops.Load(), f.closed.Load())
		}
	})
}

// The optional loopback Samba fixture verifies successful SMB establishment after
// late acquisition; it never contacts a device. Other tests need no service.
func TestPullBusyLateSMBSuccess(t *testing.T) {
	addr := os.Getenv("SEESTAR_TEST_SAMBA_ADDR")
	if addr == "" {
		t.Skip("requires container loopback Samba fixture")
	}
	synctest.Test(t, func(t *testing.T) {
		began := time.Now()
		f := &pullBusyFactory{}
		f.allocate = func(context.Context) (sessionedge.Lease, error) {
			if time.Since(began) < 36*time.Second {
				return nil, session.ErrBusy
			}
			return f, nil
		}
		s := pullBusySession(t, context.Background(), 90*time.Second, f)
		ctx, src, err := pull.OpenSMB(pull.Configure(context.Background(), pull.DefaultConfig()), func(ctx context.Context, _, _ string) (net.Conn, error) {
			c, err := s.DialPort(ctx, 445)
			if err != nil {
				return nil, err
			}
			c.Close()
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := src.List(ctx, "")
		if err != nil || len(entries) == 0 {
			t.Fatalf("SMB inventory=%v err=%v", entries, err)
		}
		if err := src.Features().Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if f.stops.Load() != 1 || f.closed.Load() != 4 {
			t.Fatalf("stops=%d closes=%d", f.stops.Load(), f.closed.Load())
		}
	})
}
