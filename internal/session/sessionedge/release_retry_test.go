package sessionedge

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"testing"
	"testing/synctest"
)

type abandonedLease struct{ calls int }

func (l *abandonedLease) Stop(ctx context.Context) error {
	l.calls++
	<-ctx.Done()
	return session.ErrReleaseAbandoned
}

func TestFinalAbandonedReleaseIsReported(t *testing.T) {
	for _, signal := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			f := newFake()
			lease := &abandonedLease{}
			factory := factoryFunc(func(id uint64, b session.Budgets) (Layers, error) {
				layers, err := f.New(id, b)
				layers.Cloud = allocateFunc(func(context.Context) (Lease, error) { return lease, nil })
				return layers, err
			})
			in := make(chan session.Event, 8)
			out := make(ChannelSink, 16)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- Run(ctx, session.DefaultConfig(), rng{}, factory, in, out) }()
			in <- session.Event{Kind: session.Client, Client: 1}
			outcome(t, out, session.Admit)
			if signal {
				cancel()
			} else {
				in <- session.Event{Kind: session.Shutdown}
			}
			err := <-result
			if !errors.Is(err, session.ErrStop) {
				t.Fatalf("signal=%v final release error=%v, want ErrStop", signal, err)
			}
			if signal && !errors.Is(err, context.Canceled) {
				t.Fatalf("signal cancellation lost: %v", err)
			}
			if lease.calls != 1 {
				t.Fatalf("final stop calls=%d, want 1", lease.calls)
			}
			for l := session.Rendezvous; l < session.LayerCount; l++ {
				if f.closed[l].Load() != 1 {
					t.Fatalf("layer %d closes=%d, want 1", l, f.closed[l].Load())
				}
			}
		})
	}
}

type outcomeLease struct{ err error }

func (l outcomeLease) Stop(context.Context) error { return l.err }

// Release markers are bookkeeping, never permission to ignore a stronger
// disposition delivered with them. Exercise every ordering at the real worker.
func TestReleaseDispositionCannotBeLowered(t *testing.T) {
	for _, primary := range []error{session.ErrStop, session.ErrUncertain, session.ErrBusy} {
		for _, causes := range [][]error{{primary, session.ErrReleaseAbandoned}, {session.ErrReleaseAbandoned, primary}} {
			f := newFake()
			factory := factoryFunc(func(id uint64, b session.Budgets) (Layers, error) {
				layers, err := f.New(id, b)
				layers.Cloud = allocateFunc(func(context.Context) (Lease, error) { return outcomeLease{errors.Join(causes...)}, nil })
				return layers, err
			})
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			out, done := make(chan completion, 8), make(chan struct{})
			attempt(ctx, 1, session.DefaultConfig(), factory, out, done)
			<-done
			close(out)
			var release error
			for result := range out {
				if result.event.Kind == session.Released {
					release = result.event.Err
				}
			}
			if !errors.Is(release, session.ErrStop) {
				t.Errorf("release of %v=%v, want blocking stop", causes, release)
			}
		}
	}
}
