package sessionedge

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"testing"
	"testing/synctest"
	"unsafe"
)

func TestInboxBudgetAcceptance(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := session.DefaultConfig()
		cfg.Budgets.Inbox.Bytes = 1024
		t.Logf("completion size=%d", unsafe.Sizeof(completion{}))
		f := newFake()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- Run(ctx, cfg, rng{}, f, nil, sinkFunc(func(session.Action) error { return nil })) }()
		synctest.Wait()
		cancel()
		err := <-done
		if errors.Is(err, session.ErrBudget) {
			t.Errorf("previously accepted 1024-byte inbox now rejected: %v", err)
		}
		if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
			t.Errorf("allocations/stops=%d/%d want 1/1", f.allocated.Load(), f.stopped.Load())
		}
	})
}
