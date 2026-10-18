package sessionedge

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type panicEvents struct {
	Channels
	before func()
}

func (p panicEvents) Events() <-chan session.Event {
	if p.before != nil {
		p.before()
	}
	panic("private accessor failure")
}

type rendezvousFunc func(context.Context, Lease) (Transport, error)

func (f rendezvousFunc) Dial(ctx context.Context, l Lease) (Transport, error) { return f(ctx, l) }

type reliableFunc func(context.Context, Transport) (Transport, error)

func (f reliableFunc) Create(ctx context.Context, tr Transport) (Transport, error) { return f(ctx, tr) }

type constructionFault struct {
	name     string
	layer    int // -1 is Factory.New
	accessor bool
	cause    error
	before   func()
}

// Real panicking adapters complement the pure property, whose fault menu only
// models delivered outcomes. Every construction callback and Events is exercised.
func constructionFaults() []constructionFault {
	faults := []constructionFault{{name: "factory-panic", layer: -1}}
	for l := session.Cloud; l < session.LayerCount; l++ {
		faults = append(faults, constructionFault{name: fmt.Sprintf("constructor-panic/%d", l), layer: int(l)})
		if l == session.Cloud {
			continue
		}
		for _, cause := range []error{nil, session.ErrBusy, session.ErrUncertain, session.ErrStop} {
			faults = append(faults, constructionFault{name: fmt.Sprintf("events-panic/%d/%v", l, cause), layer: int(l), accessor: true, cause: cause})
		}
	}
	return faults
}

func faultFactory(f *fake, fault constructionFault) Factory {
	return factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
		if fault.layer == -1 {
			if fault.before != nil {
				fault.before()
			}
			panic("private factory failure")
		}
		l, _ := f.New(a, b)
		if !fault.accessor {
			f.panicAt = fault.layer
			f.panicHook = fault.before
			return l, nil
		}
		construct := func(ctx context.Context) (Transport, error) {
			tr, _ := f.transport(ctx, session.Layer(fault.layer))
			return panicEvents{tr.(Channels), fault.before}, fault.cause
		}
		switch session.Layer(fault.layer) {
		case session.Rendezvous:
			l.Rendezvous = rendezvousFunc(func(ctx context.Context, _ Lease) (Transport, error) { return construct(ctx) })
		case session.DTLS:
			l.DTLS = reviewHandshake(func(ctx context.Context, _ Transport) (Transport, error) { return construct(ctx) })
		case session.RDT:
			l.RDT = reliableFunc(func(ctx context.Context, _ Transport) (Transport, error) { return construct(ctx) })
		case session.Tunnel:
			l.Tunnel = tunnelFunc(func(ctx context.Context, _ Transport) (Channels, error) {
				tr, err := construct(ctx)
				return tr.(Channels), err
			})
		}
		return l, nil
	})
}

func TestConstructionPanicTerminalOutcome(t *testing.T) {
	for _, fault := range constructionFaults() {
		t.Run(fault.name, func(t *testing.T) {
			f := newFake()
			out, done := make(chan completion, 8), make(chan struct{})
			go attempt(context.Background(), 1, session.DefaultConfig(), faultFactory(f, fault), out, done)
			<-done
			failed, released := false, false
			for len(out) > 0 {
				e := (<-out).event
				switch e.Kind {
				case session.Failed:
					failed = true
					if !errors.Is(e.Err, ErrPanic) || (!errors.Is(e.Err, session.ErrBusy) && !errors.Is(e.Err, session.ErrUncertain) && !errors.Is(e.Err, session.ErrStop)) {
						t.Errorf("panic is not terminal: %v", e.Err)
					}
					if fault.cause != nil && !errors.Is(e.Err, fault.cause) {
						t.Errorf("constructor result lost: %v want %v", e.Err, fault.cause)
					}
					if strings.Contains(e.Err.Error(), "private") {
						t.Errorf("unsanitized failure: %v", e.Err)
					}
				case session.Released:
					released = true
					if !errors.Is(e.Err, session.ErrStop) && !errors.Is(e.Err, session.ErrUncertain) {
						t.Errorf("unknown panic outcome permitted retry: %v", e.Err)
					}
				}
			}
			if !failed || !released {
				t.Fatalf("failure/release=%v/%v", failed, released)
			}
			assertCleanupOrder(t, f.cleanupOrder, f.resources, f, f.allocated.Load() != 0)
		})
	}
}

func TestSeededConstructionFaults(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		r := rand.New(rand.NewPCG(seed, 31))
		faults := constructionFaults()
		r.Shuffle(len(faults), func(i, j int) { faults[i], faults[j] = faults[j], faults[i] })
		for _, fault := range faults {
			// Vary lazy demand and whether final collection races worker cleanup or
			// follows an already processed Released. No automatic retry permits reacquisition.
			lazy, cancelEarly := r.IntN(2) == 0, r.IntN(2) == 0
			t.Run(fmt.Sprintf("%d/%s", seed, fault.name), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					f := newFake()
					entered, finish := make(chan struct{}), make(chan struct{})
					fault.before = func() { close(entered); <-finish }
					factory := faultFactory(f, fault)
					var calls int
					wrapped := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
						calls++
						return factory.New(a, b)
					})
					cfg := session.DefaultConfig()
					if lazy {
						cfg.Idle = time.Second
					}
					in, out := make(chan session.Event, 8), make(ChannelSink, 16)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					done := make(chan error, 1)
					go func() { done <- Run(ctx, cfg, rng{}, wrapped, in, out) }()
					if lazy {
						in <- session.Event{Kind: session.Client, Client: 1}
					}
					<-entered
					if cancelEarly {
						cancel()
						synctest.Wait()
					}
					close(finish)
					if !cancelEarly {
						synctest.Wait()
						<-time.After(3 * time.Second)
						synctest.Wait()
						if calls != 1 {
							t.Errorf("panic retried automatically: factory calls=%d want 1", calls)
						}
						in <- session.Event{Kind: session.Client, Client: 2}
						a := outcome(t, out, session.Reject)
						if !errors.Is(a.Err, session.ErrUncertain) && !errors.Is(a.Err, session.ErrStop) {
							t.Errorf("panic rejection=%v want uncertainty", a.Err)
						}
						cancel()
					}
					err := <-done
					if !errors.Is(err, context.Canceled) || !errors.Is(err, session.ErrStop) {
						t.Errorf("panic final collection=%v want cancellation and ErrStop", err)
					}
					if calls != 1 || f.allocated.Load() != f.stopped.Load() {
						t.Errorf("calls=%d allocations/stops=%d/%d", calls, f.allocated.Load(), f.stopped.Load())
					}
					assertCleanupOrder(t, f.cleanupOrder, f.resources, f, f.allocated.Load() != 0)
				})
			})
		}
	}
}
