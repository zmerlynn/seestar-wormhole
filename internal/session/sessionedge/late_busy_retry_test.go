package sessionedge

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestLateBusyDoesNotRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		cfg := session.DefaultConfig()
		cfg.DialTimeout = time.Second
		var calls atomic.Int32
		factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
			l, _ := f.New(a, b)
			l.Cloud = allocateFunc(func(ctx context.Context) (Lease, error) { calls.Add(1); <-ctx.Done(); return nil, session.ErrBusy })
			return l, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Run(ctx, cfg, rng{}, factory, nil, make(ChannelSink, 16)) }()
		synctest.Wait()
		<-time.After(3 * time.Second)
		synctest.Wait()
		got := calls.Load()
		cancel()
		<-done
		if got != 1 {
			t.Fatalf("Allocate calls=%d, want 1 after busy (sticky busy)", got)
		}
	})
}
