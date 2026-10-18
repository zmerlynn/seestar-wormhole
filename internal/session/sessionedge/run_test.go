package sessionedge

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type rng struct{}

func (rng) Uint64() uint64 { return 0 }

type sinkFunc func(session.Action) error

func (f sinkFunc) Try(a session.Action) error { return f(a) }
func outcome(t *testing.T, out <-chan session.Action, k session.Effect) session.Action {
	t.Helper()
	for {
		a := <-out
		if a.Kind == k {
			return a
		}
	}
}
func TestRunExitPathsStopOnce(t *testing.T) {
	for _, exit := range []string{"normal", "signal", "input", "sink-error", "panic"} {
		t.Run(exit, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFake()
				in := make(chan session.Event, 8)
				out := make(chan session.Action, 16)
				result := make(chan error, 1)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				sink := sinkFunc(func(a session.Action) error {
					if a.Kind == session.Disconnect {
						switch exit {
						case "sink-error":
							return errors.New("output full")
						case "panic":
							panic("private sink panic")
						}
					}
					out <- a
					return nil
				})
				go func() { result <- Run(ctx, session.DefaultConfig(), rng{}, f, in, sink) }()
				in <- session.Event{Kind: session.Client, Client: 1}
				outcome(t, out, session.Admit)
				switch exit {
				case "signal":
					cancel()
				case "input":
					close(in)
				default:
					in <- session.Event{Kind: session.Shutdown}
				}
				err := <-result
				if exit == "normal" && err != nil {
					t.Fatal(err)
				}
				if exit == "panic" && err != ErrPanic {
					t.Fatalf("panic = %v", err)
				}
				if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
					t.Fatalf("exit %s allocations=%d stops=%d", exit, f.allocated.Load(), f.stopped.Load())
				}
			})
		})
	}
}
func TestRunBusyNoRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		f.fail = 1
		f.busy = true
		in := make(chan session.Event, 8)
		out := make(chan session.Action, 16)
		result := make(chan error, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			result <- Run(ctx, session.DefaultConfig(), rng{}, f, in, sinkFunc(func(a session.Action) error { out <- a; return nil }))
		}()
		// Let eager startup finish its known-busy cleanup before this arrival.
		synctest.Wait()
		in <- session.Event{Kind: session.Client, Client: 1}
		if a := outcome(t, out, session.Reject); a.Err != session.ErrBusy {
			t.Fatalf("busy error = %v", a.Err)
		}
		synctest.Wait()
		<-time.After(8 * time.Hour)
		synctest.Wait()
		if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
			t.Fatal("busy retried or failed to release")
		}
		in <- session.Event{Kind: session.Shutdown}
		if err := <-result; !errors.Is(err, session.ErrBusy) {
			t.Fatal(err)
		}
		if len(f.contexts) != 1 || f.contexts[0].Err() == nil {
			t.Fatal("busy attempt context left live")
		}
		if f.stopped.Load() != 1 {
			t.Fatal("busy allocation stopped twice")
		}
	})
}

type allocateFunc func(context.Context) (Lease, error)

func (f allocateFunc) Allocate(c context.Context) (Lease, error) { return f(c) }

type factoryFunc func(uint64, session.Budgets) (Layers, error)

func (f factoryFunc) New(a uint64, b session.Budgets) (Layers, error) { return f(a, b) }
func TestRunShutdownRacingAllocation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		entered := make(chan struct{})
		in := make(chan session.Event, 8)
		out := make(chan session.Action, 16)
		result := make(chan error, 1)
		factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
			l, _ := f.New(a, b)
			l.Cloud = allocateFunc(func(ctx context.Context) (Lease, error) {
				close(entered)
				<-ctx.Done()
				f.allocated.Add(1)
				return f, ctx.Err()
			})
			return l, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			result <- Run(ctx, session.DefaultConfig(), rng{}, factory, in, sinkFunc(func(a session.Action) error { out <- a; return nil }))
		}()
		<-entered
		in <- session.Event{Kind: session.Shutdown}
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if f.stopped.Load() != 1 {
			t.Fatal("late allocation stopped twice")
		}
	})
}
func TestRunLayerFailureRedials(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		f.notices[session.DTLS] = make(chan session.Event, 1)
		in := make(chan session.Event, 8)
		out := make(chan session.Action, 32)
		result := make(chan error, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			result <- Run(ctx, session.DefaultConfig(), rng{}, f, in, sinkFunc(func(a session.Action) error { out <- a; return nil }))
		}()
		in <- session.Event{Kind: session.Client, Client: 1}
		a := outcome(t, out, session.Admit)
		f.notices[session.DTLS] <- session.Event{Kind: session.Failed}
		outcome(t, out, session.Disconnect)
		synctest.Wait()
		if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
			t.Fatal("failure skipped release or backoff")
		}
		<-time.After(session.DefaultConfig().BackoffMin)
		synctest.Wait()
		in <- session.Event{Kind: session.Client, Client: 2}
		b := outcome(t, out, session.Admit)
		if b.Attempt <= a.Attempt {
			t.Fatal("redial did not advance generation")
		}
		in <- session.Event{Kind: session.Shutdown}
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if f.stopped.Load() != 2 {
			t.Fatal("redial leaked allocation")
		}
		if len(f.contexts) != 2 || f.contexts[0] == f.contexts[1] || f.contexts[0].Err() == nil || f.contexts[1].Err() == nil {
			t.Fatal("retry contexts reused or left live")
		}
	})
}

type tunnelFunc func(context.Context, Transport) (Channels, error)

func (f tunnelFunc) Start(c context.Context, t Transport) (Channels, error) { return f(c, t) }

type rejectChannels struct{ Channels }

func (rejectChannels) TryOpen(uint64) error { return session.ErrBudget }

func TestRejectedClientStillExpiresLazyIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		cfg := session.DefaultConfig()
		cfg.Idle = time.Second
		factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
			l, _ := f.New(a, b)
			l.Tunnel = tunnelFunc(func(ctx context.Context, tr Transport) (Channels, error) {
				ch, err := f.Start(ctx, tr)
				return rejectChannels{ch}, err
			})
			return l, nil
		})
		in := make(chan session.Event, 8)
		out := make(chan session.Action, 16)
		result := make(chan error, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { result <- Run(ctx, cfg, rng{}, factory, in, ChannelSink(out)) }()
		in <- session.Event{Kind: session.Client, Client: 1}
		outcome(t, out, session.Reject)
		<-time.After(2 * time.Second)
		synctest.Wait()
		if f.stopped.Load() != 1 {
			t.Fatal("rejected client lost idle timer and retained allocation")
		}
		cancel()
		<-result
	})
}
func TestNotificationQueueFitsInboxReservation(t *testing.T) {
	cfg := session.DefaultConfig()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, cfg, rng{}, newFake(), nil, make(ChannelSink, cfg.Budgets.Inbox.Count))
	if err != session.ErrBudget {
		t.Fatalf("oversubscribed notification budget accepted: %v", err)
	}
}

func TestCancellationAtEveryConstructionPhase(t *testing.T) {
	for l := session.Cloud; l < session.LayerCount; l++ {
		synctest.Test(t, func(t *testing.T) {
			f := newFake()
			f.blockAt = int(l)
			f.entered = make(chan int, 1)
			f.partial = true
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			go func() { result <- Run(ctx, session.DefaultConfig(), rng{}, f, nil, make(ChannelSink, 16)) }()
			<-f.entered
			cancel()
			if err := <-result; err != context.Canceled {
				t.Fatalf("phase %v cancellation = %v", l, err)
			}
			if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
				t.Fatalf("phase %v allocations=%d stops=%d", l, f.allocated.Load(), f.stopped.Load())
			}
		})
	}
}
func TestCleanupPanicStillStopsCloud(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		f.panicCloseAt = int(session.Tunnel)
		in := make(chan session.Event, 8)
		out := make(ChannelSink, 16)
		result := make(chan error, 1)
		go func() { result <- Run(context.Background(), session.DefaultConfig(), rng{}, f, in, out) }()
		in <- session.Event{Kind: session.Client, Client: 1}
		outcome(t, out, session.Admit)
		in <- session.Event{Kind: session.Shutdown}
		if err := <-result; err != session.ErrStop {
			t.Fatalf("cleanup panic result = %v", err)
		}
		if f.stopped.Load() != 1 {
			t.Fatal("cleanup panic skipped cloud stop")
		}
		for l := session.Rendezvous; l < session.LayerCount; l++ {
			if f.closed[l].Load() != 1 {
				t.Fatalf("layer %v not closed after cleanup panic", l)
			}
		}
	})
}
func TestBlockedNotificationOutputStillReleases(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		in := make(chan session.Event, 1)
		in <- session.Event{Kind: session.Client, Client: 1}
		err := Run(context.Background(), session.DefaultConfig(), rng{}, f, in, make(ChannelSink))
		if err != ErrAdapter || f.stopped.Load() != 1 {
			t.Fatalf("blocked output: error=%v stops=%d", err, f.stopped.Load())
		}
	})
}
func TestRunStuckStopBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		f.stuckStop = true
		cfg := session.DefaultConfig()
		in := make(chan session.Event, 8)
		out := make(ChannelSink, 16)
		result := make(chan error, 1)
		go func() { result <- Run(context.Background(), cfg, rng{}, f, in, out) }()
		in <- session.Event{Kind: session.Client, Client: 1}
		outcome(t, out, session.Admit)
		start := time.Now()
		in <- session.Event{Kind: session.Shutdown}
		err := <-result
		if !errors.Is(err, session.ErrStop) && !errors.Is(err, session.ErrTimeout) {
			t.Fatalf("stuck stop result = %v", err)
		}
		if elapsed := time.Since(start); elapsed != cfg.StopTimeout {
			t.Fatalf("local shutdown = %v, want %v", elapsed, cfg.StopTimeout)
		}
		if f.stopped.Load() != 1 {
			t.Fatal("stuck stop invoked more than once")
		}
	})
}
