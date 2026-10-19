package sessionedge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"unsafe"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestInboxReservationThreshold(t *testing.T) {
	if n := unsafe.Sizeof(completion{}); n > 104 {
		t.Fatalf("completion bytes=%d exceed reserved 104", n)
	}
	for _, size := range []int{831, 832, 833, 1023, 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := session.DefaultConfig()
				cfg.Budgets.Inbox.Bytes = size
				f := newFake()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() { done <- Run(ctx, cfg, rng{}, f, nil, sinkFunc(func(session.Action) error { return nil })) }()
				synctest.Wait()
				cancel()
				err := <-done
				if size < 832 {
					if !errors.Is(err, session.ErrBudget) || f.allocated.Load() != 0 {
						t.Errorf("undersized inbox result=%v allocations=%d", err, f.allocated.Load())
					}
					return
				}
				if errors.Is(err, session.ErrBudget) || f.allocated.Load() != 1 || f.stopped.Load() != 1 {
					t.Errorf("accepted inbox result=%v allocations/stops=%d/%d", err, f.allocated.Load(), f.stopped.Load())
				}
			})
		})
	}
}
