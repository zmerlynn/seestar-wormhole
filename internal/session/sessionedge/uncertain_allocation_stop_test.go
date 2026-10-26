package sessionedge

import (
	"context"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestAllocatorStopFailureRecoversAfterDelay(t *testing.T) {
	for _, cause := range []error{session.ErrStop, session.ErrUncertain, errors.Join(session.ErrStop, session.ErrBusy), errors.Join(session.ErrUncertain, session.ErrTimeout)} {
		t.Run(fmt.Sprint(cause), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFake()
				var calls atomic.Int32
				factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
					l, _ := f.New(a, b)
					l.Cloud = allocateFunc(func(context.Context) (Lease, error) {
						if calls.Add(1) == 1 {
							return nil, cause
						}
						return f, nil
					})
					return l, nil
				})
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, factory, nil, make(ChannelSink, 16)) }()
				synctest.Wait()
				<-time.After(2 * time.Second)
				synctest.Wait()
				if got := calls.Load(); got != 1 {
					t.Fatalf("early retry: calls=%d", got)
				}
				<-time.After(58 * time.Second)
				synctest.Wait()
				if got := calls.Load(); got != 2 {
					t.Fatalf("Allocate calls=%d, want fresh allocation after delay", got)
				}
				cancel()
				if err := <-done; err != context.Canceled {
					t.Fatalf("successful recovery retained cleanup failure: %v", err)
				}
				if f.stopped.Load() != 1 {
					t.Fatal("fresh lease not released on shutdown")
				}
			})
		})
	}
}

func TestUncertainAllocatorOwnership(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, cause := range []error{session.ErrStop, session.ErrUncertain, errors.Join(session.ErrBusy, fmt.Errorf("private: %w", session.ErrStop))} {
			f := newFake()
			factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
				l, _ := f.New(a, b)
				l.Cloud = allocateFunc(func(context.Context) (Lease, error) {
					if partial {
						f.allocated.Add(1)
						return f, cause
					}
					// Allocator retained ownership and attempted its one internal stop.
					f.allocated.Add(1)
					f.stopped.Add(1)
					return nil, cause
				})
				return l, nil
			})
			out := make(chan completion, 8)
			done := make(chan struct{})
			go attempt(context.Background(), 1, session.DefaultConfig(), factory, out, done)
			<-done
			if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
				t.Fatal("partial/internal allocation stopped twice or leaked")
			}
			for len(out) > 0 {
				e := (<-out).event
				if (e.Kind == session.Failed || e.Kind == session.Released) && !errors.Is(e.Err, session.ErrStop) && !errors.Is(e.Err, session.ErrUncertain) {
					t.Fatalf("uncertainty lost: %+v", e)
				}
			}
		}
	}
}

func TestAllocatorPanicAfterInternalCleanupRemainsUncertain(t *testing.T) {
	f := newFake()
	factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
		l, _ := f.New(a, b)
		l.Cloud = allocateFunc(func(context.Context) (Lease, error) {
			f.allocated.Add(1)
			defer func() { f.stopped.Add(1); panic("private internal stop failure") }()
			panic("private allocation failure")
		})
		return l, nil
	})
	out := make(chan completion, 8)
	done := make(chan struct{})
	go attempt(context.Background(), 1, session.DefaultConfig(), factory, out, done)
	<-done
	failed, released := <-out, <-out
	if !errors.Is(failed.event.Err, ErrPanic) || !errors.Is(failed.event.Err, session.ErrUncertain) || !errors.Is(released.event.Err, session.ErrUncertain) {
		t.Fatalf("allocation panic lost uncertainty: failure=%+v release=%+v", failed, released)
	}
	if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
		t.Fatal("allocator internal cleanup was duplicated")
	}
}

func TestPermanentOutcomesDoNotRecoverOwnership(t *testing.T) {
	for _, reason := range []string{"unauthorized", "reauthentication_required", "rejected", "cloud_license_missing", "invalid_response", "invalid_configuration", "invalid_license", "invalid_identifier", "invalid_frame", "inconsistent server answer: detail identity differs from requested pair"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFake()
				var calls atomic.Int32
				factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
					l, _ := f.New(a, b)
					l.Cloud = allocateFunc(func(context.Context) (Lease, error) {
						calls.Add(1)
						return nil, diagnostic.NewTerminal(session.Cloud, reason, session.ErrUncertain)
					})
					return l, nil
				})
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, factory, nil, make(ChannelSink, 16)) }()
				synctest.Wait()
				<-time.After(2 * time.Hour)
				synctest.Wait()
				if calls.Load() != 1 {
					t.Fatalf("permanent outcome retried: %d", calls.Load())
				}
				cancel()
				err := <-done
				if !errors.Is(err, session.ErrStop) {
					t.Fatalf("lost cleanup reporting: %v", err)
				}
				if _, got := diagnostic.Fields(err, ""); got != reason {
					t.Fatalf("lost terminal reason: %s", got)
				}
			})
		})
	}
}

func TestAllocatorPanicStillBlocksRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		f.panicAt = int(session.Cloud)
		var calls atomic.Int32
		factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) { calls.Add(1); return f.New(a, b) })
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, factory, nil, make(ChannelSink, 16)) }()
		synctest.Wait()
		<-time.After(2 * time.Minute)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Errorf("allocator panic retried: %d", calls.Load())
		}
		cancel()
		if err := <-done; !errors.Is(err, session.ErrStop) || !errors.Is(err, context.Canceled) {
			t.Fatalf("panic cleanup reporting=%v", err)
		}
	})
}
