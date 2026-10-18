package sessionedge

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type completion struct {
	event   session.Event
	payload any // Transport for progress; *residualBatch for the final failure.
	notices *eventQueue
}

// residualBatch replaces drained notification storage, one node per nonempty
// layer. A node plus N error slots costs at most N session.Event slots (N >= 1).
// Progress and residual data share completion.payload, keeping the mandatory
// eight-slot inbox footprint unchanged. No raw events or envelopes are retained.
type residualBatch struct {
	next     *residualBatch
	layer    session.Layer
	outcomes []error
}

func (r completion) residual() *residualBatch {
	batch, _ := r.payload.(*residualBatch)
	return batch
}

// eventQueue shares a terminal receipt between live consumption and teardown.
// A producer can finish construction while the owner consumes an earlier queue;
// publication is synchronized even on that failure path.
type eventQueue struct {
	events   <-chan session.Event
	terminal atomic.Pointer[diagnostic.Terminal]
}

func (q *eventQueue) channel() <-chan session.Event {
	if q == nil {
		return nil
	}
	return q.events
}

func (q *eventQueue) failed(err error, layer session.Layer) error {
	typed := diagnostic.HasReceipt(err)
	err = constructorFailure(err, layer)
	if prior := q.terminal.Load(); prior != nil {
		phase, reason := diagnostic.Fields(prior, "")
		nextPhase, nextReason := diagnostic.Fields(err, "")
		// Untyped partial-constructor echoes have no incoming receipt, so reuse
		// the constructor receipt for equal cause and disposition. Typed
		// outcomes keep their own identities even when all labels match.
		if !typed && phase == nextPhase && reason == nextReason &&
			diagnostic.Class(policy(prior)) == diagnostic.Class(policy(err)) &&
			errors.Is(prior, session.ErrReleaseAbandoned) == errors.Is(err, session.ErrReleaseAbandoned) {
			return diagnostic.RetainTerminal(prior, policy(err))
		}
	}
	if terminalPriority(nil, err) != nil {
		var terminal *diagnostic.Terminal
		errors.As(err, &terminal)
		q.terminal.CompareAndSwap(nil, terminal)
	}
	return err
}

func (q *eventQueue) closed(layer session.Layer) error {
	if terminal := q.terminal.Load(); terminal != nil {
		return terminal
	}
	err := q.failed(session.ErrTransport, layer)
	// Another consumer may have published the same closure first.
	if terminal := q.terminal.Load(); terminal != nil {
		return terminal
	}
	return err
}

// guarded contains panics at each cleanup boundary so later cleanup still runs.
func guarded(f func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
	}()
	return f()
}

// attempt has one worker and at most eight messages: five progress, one failure,
// one terminal notification aggregate, one release. Its private inbox has eight slots, so teardown never needs an
// owner or client to drain output. The owner never starts an overlapping worker.
func attempt(ctx context.Context, id uint64, cfg session.Config, f Factory, out chan<- completion, done chan<- struct{}) {
	var lease Lease
	var owned []Transport
	var notices [session.LayerCount]*eventQueue
	var failure error
	layer := session.Cloud
	defer close(done)
	defer func() {
		if recover() != nil {
			// A panic leaves the adapter's outcome unknown, including a queue
			// we could not capture. Never retry automatically, and retain any
			// sanitized constructor result already received before the callback.
			failure = errors.Join(constructorFailure(failure, layer), ErrPanic, session.ErrUncertain)
		}
		if failure != nil {
			out <- completion{event: session.Event{Kind: session.Failed, Attempt: id, Layer: failureLayer(failure, layer), Err: failure}}
		}
		// Close local resources before cloud stop, even if one adapter panics.
		var cleanup error
		if errors.Is(failure, session.ErrStop) || errors.Is(failure, session.ErrUncertain) {
			cleanup = session.ErrUncertain
			if errors.Is(failure, session.ErrStop) {
				cleanup = session.ErrStop
			}
		}
		for i := len(owned) - 1; i >= 0; i-- {
			if err := guarded(owned[i].Close); err != nil {
				cleanup = session.ErrStop
			}
		}
		// Close quiesces all producers. The owner may have detached these queues
		// on Release, or never installed a partial/late construction result.
		// Retain diagnostics from bounded residual notifications without replaying
		// admission or creating another release during teardown.
		var terminal error
		var observations *residualBatch
		for l, q := range notices {
			var batch *residualBatch
			terminal = terminalPriority(terminal, drainTerminalObserved(q, cfg.Budgets.Layers[l].Count, session.Layer(l), func(err error) {
				// Retain only bounded normalized outcomes, replacing consumed events
				// within the queue's layer reservation. Closure adds at most one outcome
				// when no received terminal exists, including an unbuffered queue.
				if batch == nil {
					bound := max(1, min(cap(q.channel()), cfg.Budgets.Layers[l].Count))
					batch = &residualBatch{next: observations, layer: session.Layer(l), outcomes: make([]error, 0, bound)}
					observations = batch
				}
				batch.outcomes = append(batch.outcomes, err)
			}))
		}
		if terminal != nil {
			out <- completion{event: session.Event{Kind: session.Failed, Attempt: id, Layer: failureLayer(terminal, layer), Err: terminal}, payload: observations}
			if errors.Is(terminal, session.ErrStop) || errors.Is(terminal, session.ErrUncertain) {
				cleanup = session.ErrStop
			}
		}
		if lease != nil {
			stopCtx, cancel := context.WithTimeout(context.Background(), cfg.StopTimeout)
			err := guarded(func() error { return lease.Stop(stopCtx) })
			cancel()
			if err != nil {
				// Interpret the canonical disposition before release bookkeeping.
				// A marker cannot weaken a received blocking stop outcome.
				if policy(err) == session.ErrReleaseAbandoned && cleanup == nil {
					cleanup = session.ErrReleaseAbandoned
				} else {
					cleanup = session.ErrStop
				}
			}
		}
		out <- completion{event: session.Event{Kind: session.Released, Attempt: id, Err: cleanup, ReleasedAllocation: lease != nil}}
	}()
	layers, err := f.New(id, cfg.Budgets)
	if err != nil {
		failure = constructorFailure(err, layer)
		return
	}
	if layers.Cloud == nil || layers.Rendezvous == nil || layers.DTLS == nil || layers.RDT == nil || layers.Tunnel == nil {
		failure = constructorFailure(ErrAdapter, layer)
		return
	}
	lease, err = layers.Cloud.Allocate(ctx)
	if err != nil {
		failure = constructorFailure(err, layer)
		return
	}
	if lease == nil {
		failure = constructorFailure(ErrAdapter, layer)
		return
	}
	out <- completion{event: session.Event{Kind: session.Progress, Attempt: id, Layer: session.Cloud}}
	var previous Transport
	for l := session.Rendezvous; l < session.LayerCount; l++ {
		layer = l
		if ctx.Err() != nil {
			return
		}
		var next Transport
		switch l {
		case session.Rendezvous:
			next, err = layers.Rendezvous.Dial(ctx, lease)
		case session.DTLS:
			next, err = layers.DTLS.Handshake(ctx, previous)
		case session.RDT:
			next, err = layers.RDT.Create(ctx, previous)
		case session.Tunnel:
			next, err = layers.Tunnel.Start(ctx, previous)
		}
		if next != nil {
			owned = append(owned, next)
		}
		// Register the partial resource, then save its outcome before invoking
		// another adapter method. Recovery must not erase a terminal result.
		if err != nil {
			failure = constructorFailure(err, layer)
		}
		if next != nil {
			// Capture once, including partial results. Events must remain stable
			// through Close; only the owner and final cleanup consume this queue.
			notices[l] = &eventQueue{events: next.Events()}
			if failure != nil {
				notices[l].failed(failure, layer)
			}
			if cap(notices[l].channel()) > cfg.Budgets.Layers[l].Count {
				failure = errors.Join(failure, session.ErrStop)
				return
			}
		}
		if failure != nil {
			return
		}
		if next == nil {
			failure = constructorFailure(ErrAdapter, layer)
			return
		}
		previous = next
		out <- completion{event: session.Event{Kind: session.Progress, Attempt: id, Layer: l}, payload: next, notices: notices[l]}
	}
	<-ctx.Done()
}

func policy(err error) error { return diagnostic.Disposition(err) }

// terminalPriority selects policy only after the caller has preserved every
// received outcome for observation. It never accumulates a receipt history.
func terminalPriority(a, b error) error {
	return diagnostic.Primary(diagnostic.JoinTerminals(a, b))
}

func drainTerminal(q *eventQueue, limit int, layer session.Layer) error {
	return drainTerminalObserved(q, limit, layer, nil)
}

func drainTerminalObserved(q *eventQueue, limit int, layer session.Layer, observe func(error)) error {
	ch := q.channel()
	if ch == nil {
		return nil
	}
	var terminal error
	receive := func(err error) {
		if terminalPriority(nil, err) != nil {
			if observe != nil {
				observe(err)
			}
			terminal = terminalPriority(terminal, err)
		}
	}
	bound := min(cap(ch), limit)
	for i := 0; i <= bound; i++ {
		// After producers join, one extra receive can detect closure, including
		// an unbuffered queue, without consuming beyond the configured bound.
		if i == bound && len(ch) > 0 {
			return terminal
		}
		select {
		case e, ok := <-ch:
			if !ok {
				if terminal != nil || q.terminal.Load() != nil {
					return terminal
				}
				receive(q.closed(layer))
				return terminal
			}
			if e.Kind == session.Failed {
				receive(q.failed(e.Err, layer))
			}
		default:
			return terminal
		}
	}
	return terminal
}

func failureLayer(err error, fallback session.Layer) session.Layer {
	var terminal *diagnostic.Terminal
	if errors.As(err, &terminal) {
		return terminal.Origin()
	}
	return fallback
}
func constructorFailure(err error, layer session.Layer) error {
	return diagnostic.AtLayer(err, layer, policy(err))
}
