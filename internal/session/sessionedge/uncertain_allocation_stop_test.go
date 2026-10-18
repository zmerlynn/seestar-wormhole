package sessionedge

import (
	"context"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestAllocatorStopFailureDoesNotRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		var calls atomic.Int32
		factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
			l, _ := f.New(a, b)
			l.Cloud = allocateFunc(func(context.Context) (Lease, error) { calls.Add(1); return nil, session.ErrStop })
			return l, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, factory, nil, make(ChannelSink, 16)) }()
		synctest.Wait()
		<-time.After(2 * time.Second)
		synctest.Wait()
		got := calls.Load()
		cancel()
		<-done
		if got != 1 {
			t.Fatalf("Allocate calls=%d, want 1 after allocator reports failed internal stop", got)
		}
	})
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
