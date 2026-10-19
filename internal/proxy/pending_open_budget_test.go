package proxy

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// The ninth pending caller is refused, cancellation frees exactly one slot,
// and recovery opens only the eight remaining owners (never the canceled one).
func TestPendingOpenCountAndCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var offline atomic.Bool
		s, stacks, cloud := workloadSession(t, ctx, func(p *simulatedWire) { p.verify = true; p.outage = offline.Load })
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		x := <-stacks
		offline.Store(true)
		results := make(chan error, 9)
		start := func(ctx context.Context) {
			go func() {
				c, err := s.DialPort(ctx, 4700)
				if c != nil {
					c.Close()
				}
				results <- err
			}()
		}
		canceled, stop := context.WithCancel(ctx)
		start(canceled)
		synctest.Wait()
		for i := 1; i < 8; i++ {
			start(ctx)
		}
		synctest.Wait()
		select {
		case err := <-results:
			t.Fatalf("one of the first eight pending callers returned early: %v", err)
		default:
		}
		if c, err := s.DialPort(ctx, 4700); !errors.Is(err, session.ErrBudget) {
			if c != nil {
				c.Close()
			}
			t.Fatalf("ninth pending caller=%v, want %v", err, session.ErrBudget)
		}
		stop()
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v", err)
		}
		synctest.Wait()
		start(ctx)
		synctest.Wait()
		<-time.After(10 * time.Second)
		offline.Store(false)
		for range 8 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		synctest.Wait()
		inspectStack(t, x)
		done := make(chan struct{})
		x.post(func(core.Instant) {
			if len(x.openAdmissions) != 0 || len(x.pending) != 0 || x.tunnel.Usage().Channels != 1 {
				t.Errorf("stale open: admissions=%d retries=%d channels=%d", len(x.openAdmissions), len(x.pending), x.tunnel.Usage().Channels)
			}
			close(done)
		})
		<-done
		if cloud.allocs.Load() != 1 || cloud.stops.Load() != 0 {
			t.Fatal("short outage replaced session")
		}
		s.Close()
		assertStackWorkersExited(t, x)
		assertDTLSWorkersExited(t, x)
	})
}

func TestPendingOpenWaitExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cfg := runtimeTunnelConfig(4 << 20)
		cfg.MaxChannels = 1
		s, stacks, _ := workloadSessionWithTunnel(t, ctx, func(p *simulatedWire) { p.verify = true }, Config{}, &cfg)
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		x := <-stacks
		result := make(chan error, 1)
		start := time.Now()
		go func() {
			c, err := s.DialPort(ctx, 4700)
			if c != nil {
				c.Close()
			}
			result <- err
		}()
		synctest.Wait()
		<-time.After(35*time.Second - time.Nanosecond)
		synctest.Wait()
		select {
		case err := <-result:
			t.Fatalf("early expiry after %v: %v", time.Since(start), err)
		default:
		}
		<-time.After(time.Nanosecond)
		synctest.Wait()
		select {
		case err := <-result:
			if !errors.Is(err, session.ErrTimeout) {
				t.Fatalf("expiry=%v, want %v", err, session.ErrTimeout)
			}
		default:
			t.Fatal("pending open did not expire at 35 seconds")
		}
		if time.Since(start) != 35*time.Second {
			t.Fatalf("wait=%v, want 35s", time.Since(start))
		}
		c.Close()
		synctest.Wait()
		<-time.After(30 * time.Second)
		synctest.Wait()
		inspectStack(t, x)
		done := make(chan struct{})
		x.post(func(core.Instant) {
			if len(x.openAdmissions) != 0 || len(x.pending) != 0 || x.tunnel.Usage().Channels != 0 {
				t.Errorf("expired demand revived: admissions=%d pending=%d channels=%d", len(x.openAdmissions), len(x.pending), x.tunnel.Usage().Channels)
			}
			close(done)
		})
		<-done
		c, err = s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	})
}

// With the only channel occupied, each successful verification leaves the new
// caller pending. Observe requests at the peer to check the retry interval.
func TestPendingOpenRetryCadence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cfg := runtimeTunnelConfig(4 << 20)
		cfg.MaxChannels = 1
		attempts := make(chan time.Time, 16)
		var decoder p2ptunnel.Decoder
		s, stacks, _ := workloadSessionWithTunnel(t, ctx, func(p *simulatedWire) {
			p.verify = true
			p.tunnelInput = func(e p2ptunnel.Event, m *p2ptunnel.Machine) {
				if e.Kind != p2ptunnel.Stream || m.Status() != p2ptunnel.Active {
					return
				}
				for data := e.Data; len(data) > 0; {
					n, f, err := decoder.Feed(data, p2ptunnel.Established)
					if err != nil {
						t.Errorf("verification observation: %v", err)
						return
					}
					data = data[n:]
					if f != nil && f.Command == p2ptunnel.PortRequest {
						select {
						case attempts <- time.Now():
						default:
							t.Error("verification observation overflow")
						}
					}
				}
			}
		}, Config{}, &cfg)
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		x := <-stacks
		synctest.Wait()
		// Discard the established client's verification, before timing the new one.
		select {
		case <-attempts:
		default:
			t.Fatal("established client had no verification attempt")
		}
		pendingCtx, stop := context.WithCancel(ctx)
		defer stop()
		result := make(chan error, 1)
		start := time.Now()
		go func() {
			c, err := s.DialPort(pendingCtx, 4700)
			if c != nil {
				c.Close()
			}
			result <- err
		}()
		expectAttempt := func(want time.Duration) {
			t.Helper()
			synctest.Wait()
			select {
			case at := <-attempts:
				if got := at.Sub(start); got != want {
					t.Fatalf("verification attempt at %v, want %v", got, want)
				}
			default:
				t.Fatalf("no verification attempt at %v", want)
			}
		}
		expectAttempt(0)
		for attempt := 1; attempt <= 2; attempt++ {
			<-time.After(time.Second - time.Nanosecond)
			synctest.Wait()
			select {
			case at := <-attempts:
				t.Fatalf("early verification retry at %v, want %vs", at.Sub(start), attempt)
			default:
			}
			<-time.After(time.Nanosecond)
			expectAttempt(time.Duration(attempt) * time.Second)
		}
		select {
		case err := <-result:
			t.Fatalf("pending caller returned during verification retries: %v", err)
		default:
		}
		stop()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel=%v, want %v", err, context.Canceled)
		}
		s.Close()
		assertStackWorkersExited(t, x)
		assertDTLSWorkersExited(t, x)
	})
}

func TestPendingOpenSessionEnd(t *testing.T) {
	for _, release := range []bool{false, true} {
		name := "failure"
		if release {
			name = "release"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var offline atomic.Bool
				s, stacks, _ := workloadSession(t, ctx, func(p *simulatedWire) { p.verify = true; p.outage = offline.Load })
				defer s.Close()
				c, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				x := <-stacks
				offline.Store(true)
				result := make(chan error, 1)
				go func() {
					c, err := s.DialPort(ctx, 4700)
					if c != nil {
						c.Close()
					}
					result <- err
				}()
				synctest.Wait()
				start := time.Now()
				if release {
					s.Close()
				} else {
					done := make(chan struct{})
					x.post(func(now core.Instant) {
						x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.TransportFailed}}, now)
						close(done)
					})
					<-done
				}
				if err := <-result; err == nil {
					t.Fatal("session end admitted pending client")
				}
				synctest.Wait()
				if _, err := s.DialPort(ctx, 4700); err == nil {
					t.Fatal("session end admitted new client")
				}
				if time.Since(start) > time.Second {
					t.Fatalf("session end waited for admission timeout: %v", time.Since(start))
				}
				s.Close()
				assertStackWorkersExited(t, x)
				assertDTLSWorkersExited(t, x)
			})
		})
	}
}
