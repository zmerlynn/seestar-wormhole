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

type fake struct {
	fail, panicAt, blockAt, panicCloseAt int
	entered                              chan int
	panicHook                            func()
	partial, busy, stuckStop             bool
	allocated, stopped                   atomic.Int32
	closed                               [session.LayerCount]atomic.Int32
	resources                            []*fakeTransport
	cleanupOrder                         []any // worker-owned identities; inspected only after join
	failClose                            bool
	contexts                             []context.Context // worker-owned; inspected after join
	notices                              [session.LayerCount]chan session.Event
}
type fakeTransport struct {
	f      *fake
	layer  session.Layer
	closes atomic.Int32
}

func (t *fakeTransport) Close() error {
	t.f.closed[t.layer].Add(1)
	t.closes.Add(1)
	t.f.cleanupOrder = append(t.f.cleanupOrder, t)
	if t.f.panicCloseAt == int(t.layer) {
		panic("close panic")
	}
	if t.f.failClose {
		return errors.New("private close failure")
	}
	return nil
}
func (t *fakeTransport) Events() <-chan session.Event       { return t.f.notices[t.layer] }
func (t *fakeTransport) TryOpen(uint64) error               { return nil }
func (t *fakeTransport) TryClose(uint64)                    {}
func (f *fake) New(uint64, session.Budgets) (Layers, error) { return Layers{f, f, f, f, f}, nil }
func (f *fake) check(ctx context.Context, l int) error {
	if f.blockAt == l {
		f.entered <- l
		<-ctx.Done()
		return ctx.Err()
	}
	if f.panicAt == l {
		if f.panicHook != nil {
			f.panicHook()
		}
		panic("secret panic material")
	}
	if f.fail == l {
		if f.busy {
			return session.ErrBusy
		}
		return errors.New("secret layer error")
	}
	return nil
}
func (f *fake) Allocate(ctx context.Context) (Lease, error) {
	f.contexts = append(f.contexts, ctx)
	err := f.check(ctx, 0)
	if err != nil && !f.partial {
		return nil, err
	}
	f.allocated.Add(1)
	return f, err
}
func (f *fake) Stop(ctx context.Context) error {
	f.stopped.Add(1)
	f.cleanupOrder = append(f.cleanupOrder, f)
	if f.stuckStop {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func (f *fake) transport(ctx context.Context, l session.Layer) (Transport, error) {
	err := f.check(ctx, int(l))
	if err != nil && !f.partial {
		return nil, err
	}
	tr := &fakeTransport{f: f, layer: l}
	f.resources = append(f.resources, tr)
	return tr, err
}
func (f *fake) Dial(ctx context.Context, _ Lease) (Transport, error) {
	return f.transport(ctx, session.Rendezvous)
}
func (f *fake) Handshake(ctx context.Context, _ Transport) (Transport, error) {
	return f.transport(ctx, session.DTLS)
}
func (f *fake) Create(ctx context.Context, _ Transport) (Transport, error) {
	return f.transport(ctx, session.RDT)
}
func (f *fake) Start(ctx context.Context, _ Transport) (Channels, error) {
	t, err := f.transport(ctx, session.Tunnel)
	if t == nil {
		return nil, err
	}
	return t.(Channels), err
}
func newFake() *fake { return &fake{fail: -1, panicAt: -1, blockAt: -1, panicCloseAt: -1} }

func TestAttemptEveryFailureAndPanic(t *testing.T) {
	for l := 0; l < int(session.LayerCount); l++ {
		for _, panics := range []bool{false, true} {
			for _, partial := range []bool{false, true} {
				f := newFake()
				f.partial = partial
				if panics {
					f.panicAt = l
				} else {
					f.fail = l
				}
				out := make(chan completion, 8)
				done := make(chan struct{})
				go attempt(context.Background(), 7, session.DefaultConfig(), f, out, done)
				<-done
				if f.allocated.Load() != f.stopped.Load() {
					t.Fatalf("layer %d panic=%v partial=%v: allocations=%d stops=%d", l, panics, partial, f.allocated.Load(), f.stopped.Load())
				}
				assertCleanupOrder(t, f.cleanupOrder, f.resources, f, f.allocated.Load() != 0)
				seenFailure, seenRelease := false, false
				for len(out) > 0 {
					r := <-out
					if r.event.Kind == session.Failed {
						seenFailure = true
						if panics && !errors.Is(r.event.Err, ErrPanic) {
							t.Fatal("panic not sanitized")
						}
					}
					if r.event.Kind == session.Released {
						if r.event.ReleasedAllocation != (f.stopped.Load() != 0) {
							t.Fatalf("release ownership=%v stops=%d", r.event.ReleasedAllocation, f.stopped.Load())
						}
						seenRelease = true
					}
				}
				if !seenFailure || !seenRelease {
					t.Fatalf("layer %d: failure=%v release=%v", l, seenFailure, seenRelease)
				}
				for i := 1; i < int(session.LayerCount); i++ {
					want := int32(0)
					if i < l || (i == l && partial && !panics) {
						want = 1
					}
					if f.closed[i].Load() != want {
						t.Fatalf("layer %d after failure at %d panic=%v partial=%v: closes=%d want=%d", i, l, panics, partial, f.closed[i].Load(), want)
					}
				}
			}
		}
	}
}
func TestAttemptCancellationAndBoundedStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		f.stuckStop = true
		ctx, cancel := context.WithCancel(context.Background())
		out := make(chan completion, 8)
		done := make(chan struct{})
		go attempt(ctx, 1, session.DefaultConfig(), f, out, done)
		for i := 0; i < int(session.LayerCount); i++ {
			if r := <-out; r.event.Kind != session.Progress {
				t.Fatalf("progress = %+v", r)
			}
		}
		start := time.Now()
		cancel()
		<-done
		if elapsed := time.Since(start); elapsed != session.DefaultConfig().StopTimeout {
			t.Fatalf("stop time = %s", elapsed)
		}
		if r := <-out; r.event.Kind != session.Released || r.event.Err != session.ErrStop {
			t.Fatalf("release = %+v", r)
		}
		if f.stopped.Load() != 1 {
			t.Fatalf("stop count = %d", f.stopped.Load())
		}
		for l := session.Rendezvous; l < session.LayerCount; l++ {
			if f.closed[l].Load() != 1 {
				t.Fatalf("close count layer %v = %d", l, f.closed[l].Load())
			}
		}
	})
}

func TestPartialTransportIdentitiesAcrossRetries(t *testing.T) {
	f := newFake()
	f.partial = true
	seen := map[*fakeTransport]bool{}
	for attemptID := uint64(1); attemptID <= 8; attemptID++ {
		f.fail = 1 + int(attemptID%4)
		f.failClose = attemptID%3 == 1
		f.panicCloseAt = -1
		if attemptID%3 == 2 {
			f.panicCloseAt = 1 + f.fail/2
		}
		cleanupStart := len(f.cleanupOrder)
		out := make(chan completion, 8)
		done := make(chan struct{})
		start := len(f.resources)
		go attempt(context.Background(), attemptID, session.DefaultConfig(), f, out, done)
		<-done
		returned := f.resources[start:]
		assertCleanupOrder(t, f.cleanupOrder[cleanupStart:], returned, f, true)
		if len(returned) != f.fail {
			t.Fatalf("returned=%d want=%d", len(returned), f.fail)
		}
		for _, tr := range returned {
			if seen[tr] || tr.closes.Load() != 1 {
				t.Fatalf("reused/leaked transport %p closes=%d", tr, tr.closes.Load())
			}
			seen[tr] = true
		}
		// Later cleanup must not touch resources belonging to previous attempts.
		for tr := range seen {
			if tr.closes.Load() != 1 {
				t.Fatalf("old resource %p closed again", tr)
			}
		}
	}
	if f.allocated.Load() != 8 || f.stopped.Load() != 8 {
		t.Fatalf("allocations/stops=%d/%d", f.allocated.Load(), f.stopped.Load())
	}
}

// Identity plus sequence protects dependencies, including a failed phase's
// partial transport. Recording before an error/panic proves cleanup continues.
func assertCleanupOrder(t *testing.T, got []any, returned []*fakeTransport, lease *fake, stopped bool) {
	t.Helper()
	var want []any
	for i := len(returned) - 1; i >= 0; i-- {
		want = append(want, returned[i])
	}
	if stopped {
		want = append(want, lease)
	}
	if len(got) != len(want) {
		t.Fatalf("cleanup calls=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cleanup call %d identity=%p want=%p (reverse transports then lease)", i, got[i], want[i])
		}
	}
}
