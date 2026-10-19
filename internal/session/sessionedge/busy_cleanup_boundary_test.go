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

type coincidentCleanupLease struct{ finished atomic.Bool }

func (l *coincidentCleanupLease) Stop(ctx context.Context) error {
	defer l.finished.Store(true)
	select {
	case <-time.After(5 * time.Second):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestBusyCleanupAndDemandDeadlineCoincide(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := session.DefaultConfig()
		cfg.Idle = time.Minute
		cfg.LazyBusyWindow = session.LazyBusyWindow
		cfg.DialTimeout = 120 * time.Second
		f := newFake()
		lease := &coincidentCleanupLease{}
		var calls atomic.Int32
		factory := factoryFunc(func(id uint64, b session.Budgets) (Layers, error) {
			ls, err := f.New(id, b)
			ls.Cloud = allocateFunc(func(ctx context.Context) (Lease, error) {
				if calls.Add(1) != 1 {
					t.Error("replacement allocation before successful join")
				}
				select {
				case <-time.After(85 * time.Second):
					return lease, session.ErrBusy
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			return ls, err
		})
		in := make(chan session.Event, 4)
		out := make(ChannelSink, 16)
		result := make(chan error, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { result <- Run(ctx, cfg, rng{}, factory, in, out) }()
		begin := time.Now()
		in <- session.Event{Kind: session.Client, Client: 1}
		rejection := outcome(t, out, session.Reject)
		if time.Since(begin) != 90*time.Second {
			t.Fatalf("deadline elapsed=%v want 90s: %+v", time.Since(begin), rejection)
		}
		// A Stop timeout racing expiry may report either Busy or the true stop failure.
		if !errors.Is(rejection.Err, session.ErrBusy) && !errors.Is(rejection.Err, session.ErrStop) {
			t.Fatalf("unexpected rejection=%+v", rejection)
		}
		in <- session.Event{Kind: session.Shutdown}
		shutdown := <-result
		if !lease.finished.Load() || calls.Load() != 1 {
			t.Fatalf("join lost: finished=%v calls=%d", lease.finished.Load(), calls.Load())
		}
		if shutdown != nil && !errors.Is(shutdown, session.ErrStop) {
			t.Fatalf("shutdown=%v", shutdown)
		}
	})
}

func TestLazyBusyShutdownWaitsForSuccessfulCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := session.DefaultConfig()
		cfg.Idle = time.Minute
		cfg.LazyBusyWindow = session.LazyBusyWindow
		cfg.DialTimeout = 120 * time.Second
		f := newFake()
		lease := &slowBusyLease{}
		var calls atomic.Int32
		factory := factoryFunc(func(id uint64, b session.Budgets) (Layers, error) {
			ls, err := f.New(id, b)
			ls.Cloud = allocateFunc(func(ctx context.Context) (Lease, error) {
				calls.Add(1)
				select {
				case <-time.After(89 * time.Second):
					return lease, session.ErrBusy
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			return ls, err
		})
		in := make(chan session.Event, 4)
		out := make(ChannelSink, 16)
		result := make(chan error, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { result <- Run(ctx, cfg, rng{}, factory, in, out) }()
		begin := time.Now()
		in <- session.Event{Kind: session.Client, Client: 1}
		rejection := outcome(t, out, session.Reject)
		if !errors.Is(rejection.Err, session.ErrBusy) || time.Since(begin) != 90*time.Second || lease.stopped.Load() {
			t.Fatalf("budget rejection=%+v elapsed=%v stopped=%v; want busy at 90s while cleanup is pending", rejection, time.Since(begin), lease.stopped.Load())
		}
		// Force explicit shutdown before successful cleanup, rather than relying
		// on the scheduler's ordering of coincident timer and completion events.
		in <- session.Event{Kind: session.Shutdown}
		shutdown := <-result
		if shutdown != nil || !lease.stopped.Load() || calls.Load() != 1 || time.Since(begin) != 93*time.Second {
			t.Fatalf("shutdown=%v stopped=%v attempts=%d elapsed=%v; want clean joined shutdown at 93s with one attempt", shutdown, lease.stopped.Load(), calls.Load(), time.Since(begin))
		}
	})
}
