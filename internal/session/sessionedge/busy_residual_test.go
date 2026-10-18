package sessionedge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type residualDialFunc func(context.Context, Lease) (Transport, error)

func (f residualDialFunc) Dial(c context.Context, l Lease) (Transport, error) { return f(c, l) }

type residualFailureTransport struct{ q chan session.Event }

func (tr *residualFailureTransport) Close() error { return nil }

func (tr *residualFailureTransport) Events() <-chan session.Event { return tr.q }

func TestResidualFailureCannotHideClientDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := session.DefaultConfig()
		cfg.Idle = time.Minute
		cfg.LazyBusyWindow = session.LazyBusyWindow
		cfg.DialTimeout = 120 * time.Second
		f := newFake()
		var calls atomic.Int32
		lease := &slowBusyLease{}
		factory := factoryFunc(func(id uint64, b session.Budgets) (Layers, error) {
			ls, err := f.New(id, b)
			ls.Cloud = allocateFunc(func(ctx context.Context) (Lease, error) {
				if calls.Add(1) == 1 {
					return nil, session.ErrBusy
				}
				select {
				case <-time.After(87 * time.Second):
					return lease, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
			ls.Rendezvous = residualDialFunc(func(context.Context, Lease) (Transport, error) {
				q := make(chan session.Event, 1)
				q <- session.Event{Kind: session.Failed, Err: session.ErrStop}
				// A partial constructor preserves the actual cloud origin of its error,
				// then publishes an independent local cleanup failure from its queue.
				return &residualFailureTransport{q}, diagnostic.NewTerminal(session.Cloud, "busy", session.ErrBusy)
			})
			return ls, err
		})
		in := make(chan session.Event, 2)
		out := make(ChannelSink, 16)
		done := make(chan error, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { done <- Run(ctx, cfg, rng{}, factory, in, out) }()
		begin := time.Now()
		in <- session.Event{Kind: session.Client, Client: 1}
		a := outcome(t, out, session.Reject)
		elapsed := time.Since(begin)
		in <- session.Event{Kind: session.Shutdown}
		closeErr := <-done
		t.Logf("client rejection=%v elapsed=%v calls=%d stopped=%v shutdown=%v", a.Err, elapsed, calls.Load(), lease.stopped.Load(), closeErr)
		if calls.Load() != 2 || !lease.stopped.Load() {
			t.Fatalf("worker not joined or replacement attempted: calls=%d stopped=%v", calls.Load(), lease.stopped.Load())
		}
		if !errors.Is(closeErr, session.ErrStop) {
			t.Fatalf("cleanup failure lost: %v", closeErr)
		}
		if elapsed > 90*time.Second {
			t.Fatalf("client held %v despite original clean refusal at 0s, want <=90s", elapsed)
		}
	})
}
