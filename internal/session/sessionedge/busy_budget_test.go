package sessionedge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type slowBusyLease struct {
	err     error
	stopped atomic.Bool
}

func (l *slowBusyLease) Stop(ctx context.Context) error {
	select {
	case <-time.After(4 * time.Second):
		l.stopped.Store(true)
		return l.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A partial allocation at the last retry owns cleanup beyond the client budget.
// Expiry must reject demand while the owner continues joining that worker.
func TestBusyPartialLeaseCleanupBudget(t *testing.T) {
	for _, firstDelay := range []time.Duration{0, 3 * time.Second} {
		for _, stopErr := range []error{nil, session.ErrStop, session.ErrReleaseAbandoned} {
			t.Run(firstDelay.String()+"/"+map[error]string{nil: "accepted", session.ErrStop: "failed", session.ErrReleaseAbandoned: "abandoned"}[stopErr], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					cfg := session.DefaultConfig()
					cfg.Idle = time.Minute
					cfg.LazyBusyWindow = session.LazyBusyWindow
					cfg.DialTimeout = 120 * time.Second
					f := newFake()
					lease := &slowBusyLease{err: stopErr}
					var calls atomic.Int32
					factory := factoryFunc(func(id uint64, b session.Budgets) (Layers, error) {
						ls, err := f.New(id, b)
						ls.Cloud = allocateFunc(func(ctx context.Context) (Lease, error) {
							switch calls.Add(1) {
							case 1:
								if firstDelay > 0 {
									<-time.After(firstDelay)
								}
								return nil, session.ErrBusy
							case 2:
								select {
								case <-time.After(87*time.Second - firstDelay):
									return lease, session.ErrBusy
								case <-ctx.Done():
									return nil, ctx.Err()
								}
							default:
								if !lease.stopped.Load() {
									return nil, ErrAdapter
								}
								return f, nil
							}
						})
						return ls, err
					})
					in := make(chan session.Event, 4)
					out := make(ChannelSink, 16)
					done := make(chan error, 1)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					go func() { done <- Run(ctx, cfg, rng{}, factory, in, out) }()
					begin := time.Now()
					in <- session.Event{Kind: session.Client, Client: 1}
					a := outcome(t, out, session.Reject)
					if !errors.Is(a.Err, session.ErrBusy) || time.Since(begin) != 90*time.Second {
						t.Fatalf("rejection=%v elapsed=%v, want busy at %v", a.Err, time.Since(begin), 90*time.Second)
					}
					// Fresh demand during the join must never dial a successor early.
					<-time.After(time.Second)
					in <- session.Event{Kind: session.Client, Client: 2}
					synctest.Wait()
					if lease.stopped.Load() || calls.Load() != 2 {
						t.Fatalf("replacement during cleanup: stopped=%v calls=%d", lease.stopped.Load(), calls.Load())
					}
					if stopErr == nil {
						a = outcome(t, out, session.Admit)
						if a.Client != 2 || !lease.stopped.Load() || calls.Load() != 3 {
							t.Fatalf("joined replacement=%+v stopped=%v calls=%d", a, lease.stopped.Load(), calls.Load())
						}
					} else {
						a = outcome(t, out, session.Reject)
						if a.Client != 2 || !errors.Is(a.Err, session.ErrStop) || calls.Load() != 2 {
							t.Fatalf("failed cleanup allowed replacement: %+v calls=%d", a, calls.Load())
						}
					}
					in <- session.Event{Kind: session.Shutdown}
					err := <-done
					if (stopErr == nil && err != nil) || (stopErr != nil && !errors.Is(err, session.ErrStop)) {
						t.Fatalf("shutdown=%v after stop=%v", err, stopErr)
					}
				})
			})
		}
	}
}
