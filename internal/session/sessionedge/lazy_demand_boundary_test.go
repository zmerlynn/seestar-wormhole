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

func TestFreshDemandAfterOwnerDialTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := session.DefaultConfig()
		cfg.Idle = time.Minute
		cfg.LazyBusyWindow = session.LazyBusyWindow
		f := newFake()
		var calls atomic.Int32
		factory := factoryFunc(func(id uint64, b session.Budgets) (Layers, error) {
			ls, err := f.New(id, b)
			ls.Cloud = allocateFunc(func(ctx context.Context) (Lease, error) {
				if calls.Add(1) == 1 {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return nil, session.ErrBusy
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
		first := outcome(t, out, session.Reject)
		if !errors.Is(first.Err, session.ErrTimeout) || time.Since(begin) != 30*time.Second {
			t.Fatalf("first demand: %+v elapsed=%v", first, time.Since(begin))
		}
		// The owner removed the rejected client; no ClientGone is required to remove
		// it a second time. Wait past the independent cleanup/backoff before reopening.
		<-time.After(10 * time.Second)
		in <- session.Event{Kind: session.Client, Client: 2}
		secondBegin := time.Now()
		second := outcome(t, out, session.Reject)
		elapsed := time.Since(secondBegin)
		in <- session.Event{Kind: session.Shutdown}
		shutdown := <-result
		t.Logf("first rejected at 30s; second dial at 40s, rejected after %v, shutdown=%v calls=%d", elapsed, shutdown, calls.Load())
		if second.Client != 2 || !errors.Is(second.Err, session.ErrBusy) || elapsed != 90*time.Second {
			t.Fatalf("fresh demand=%+v held=%v, want busy at 130s (90s after fresh dial)", second, elapsed)
		}
	})
}
