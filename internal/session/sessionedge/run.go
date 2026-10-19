package sessionedge

import (
	"context"
	"errors"
	"math"
	"time"
	"unsafe"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Observer receives fixed-size lifecycle transitions on the protocol owner.
// It must return promptly and must never retain or log identifiers or raw errors.
type Observer interface {
	Observe(session.Event, session.Snapshot, session.Snapshot, core.Instant)
}

// TimedObserver requests an owner wakeup for lifecycle reporting even when the
// machine has no timer. ObservationDeadline uses the owner's monotonic clock;
// zero disables the wakeup. Methods run only on the owner and must return promptly.
type TimedObserver interface {
	Observer
	ObservationDeadline() core.Instant
}

type owner struct {
	cleanupErr       error
	failureErr       error // bounded diagnostic outcome of the current attempt
	cfg              session.Config
	machine          *session.Machine
	factory          Factory
	sink             Sink
	origin           time.Time
	timer            *time.Timer
	timerC           <-chan time.Time
	observationTimer *time.Timer
	observationC     <-chan time.Time
	results          chan completion
	done             chan struct{}
	cancel           context.CancelFunc
	channels         Channels
	notices          [session.LayerCount]*eventQueue
}

// Run owns exactly one device until shutdown, input closure, cancellation (including
// signal.NotifyContext), sink failure or panic. It always cancels and joins its
// attempt. On exit it waits at most DialTimeout+StopTimeout for compliant adapters;
// Go cannot forcibly stop an adapter that violates cancellation/Close contracts.
// Input contains only Client, ClientGone and Shutdown commands;
// ClientGone retains the attempt from Admit, or uses zero with a lifetime-unique
// client ID to cancel before admission. Nil input permits context-only use.
// RNG is injected and owned by this call. Config controls bounded refusal retries.
func Run(ctx context.Context, cfg session.Config, rng session.RNG, f Factory, in <-chan session.Event, sink Sink) (err error) {
	m, err := session.New(cfg, rng)
	if err != nil {
		return err
	}
	// Every internal completion has fixed metadata. Residual diagnostic batches
	// replace consumed events within their existing layer reservations. External
	// commands must not retain arbitrary Err values or buffers. Their producer owns the bounded input channel.
	slots := cap(in) + 8
	if queue, ok := sink.(ChannelSink); ok {
		if cap(queue) > cfg.Budgets.Inbox.Count-slots {
			return session.ErrBudget
		}
		slots += cap(queue)
	}
	if f == nil || sink == nil {
		return ErrAdapter
	}
	// Preserve the 104-byte inbox reservation after removing control metadata.
	if slots < 8 || slots > cfg.Budgets.Inbox.Count || slots > cfg.Budgets.Inbox.Bytes/max(104, int(unsafe.Sizeof(completion{}))) {
		return session.ErrBudget
	}
	o := &owner{cfg: cfg, machine: m, factory: f, sink: sink, origin: time.Now()}

	defer func() {
		if recover() != nil {
			err = ErrPanic
		}
		if o.timer != nil {
			o.timer.Stop()
		}
		if o.observationTimer != nil {
			o.observationTimer.Stop()
		}
		if o.cancel != nil {
			o.cancel()
		}
		if o.done != nil {
			bound := time.Duration(math.MaxInt64)
			if cfg.DialTimeout <= bound-cfg.StopTimeout {
				bound = cfg.DialTimeout + cfg.StopTimeout
			}
			timer := time.NewTimer(bound)
			defer timer.Stop()
			select {
			case <-o.done:
				o.collectCleanup()
				if observer, ok := o.sink.(Observer); ok {
					snap := o.machine.Snapshot()
					observer.Observe(session.Event{Kind: session.Released, Err: o.cleanupErr}, snap, snap, core.Instant(time.Since(o.origin)))
				}
			case <-timer.C:
				err = errors.Join(err, session.ErrTimeout, session.ErrStop)
			}
		}
		if o.cleanupErr != nil && !errors.Is(err, o.cleanupErr) {
			if err == nil {
				err = o.cleanupErr
			} else {
				err = errors.Join(err, o.cleanupErr)
			}
		}
		// A refused lazy open or ambient recovery is a handled outcome, even when signal
		// cancellation or explicit shutdown races its final publication. Cleanup and
		// ownership failures remain fatal.
		if (err == nil || errors.Is(err, context.Canceled)) && o.cleanupErr == nil && o.handledBusyFailure() {
			o.failureErr = nil
		}
		if o.failureErr != nil && !errors.Is(err, o.failureErr) {
			err = errors.Join(err, o.failureErr)
		}
	}()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var work <-chan func(core.Instant)
	if driver, ok := f.(Driver); ok {
		work = driver.Work()
	}
	stop, err := o.step(session.Event{Kind: session.Start})
	if err != nil || stop {
		return err
	}
	for {
		var e session.Event
		var l session.Layer
		var layerEvent, ok bool
		select {
		case fn, ok := <-work:
			if !ok || fn == nil {
				return ErrAdapter
			}
			fn(core.Instant(time.Since(o.origin)))
			continue
		case <-ctx.Done():
			return ctx.Err()
		case e, ok = <-in:
			if !ok {
				return ErrInputClosed
			}
			switch e.Kind {
			case session.Client, session.ClientGone, session.Shutdown:
			default:
				return ErrAdapter
			}
			e.Err = nil // Control input cannot inject layer error/lease ownership.
		case r := <-o.results:
			e = r.event
			o.observeResidual(r)
			if transport, ok := r.payload.(Transport); ok {
				// A completion racing release keeps its resource in the worker's cleanup
				// list but cannot reinstall callbacks or channel admission at the owner.
				if m.Snapshot().State == session.Dialing {
					ch := r.notices
					if cap(ch.channel()) > cfg.Budgets.Layers[e.Layer].Count {
						return session.ErrBudget
					}
					o.notices[e.Layer] = ch
					if e.Layer == session.Tunnel {
						o.channels = transport.(Channels)
					}
				}
			}
			if e.Kind == session.Released {
				// A bounded cloud failure was recorded by the adapter and observer.
				// It must not poison a later successful daemon shutdown.
				if !errors.Is(e.Err, session.ErrReleaseAbandoned) || o.handledBusyFailure() {
					o.recordCleanup(e.Err)
				}
				<-o.done
				o.done = nil
				o.cancel = nil
				o.results = nil
			}
		case <-o.observationC:
			e = session.Event{Kind: session.Tick}
		case <-o.timerC:
			e = session.Event{Kind: session.Tick}
		case e, ok = <-o.notices[session.Rendezvous].channel():
			l = session.Rendezvous
			layerEvent = true
		case e, ok = <-o.notices[session.DTLS].channel():
			l = session.DTLS
			layerEvent = true
		case e, ok = <-o.notices[session.RDT].channel():
			l = session.RDT
			layerEvent = true
		case e, ok = <-o.notices[session.Tunnel].channel():
			l = session.Tunnel
			layerEvent = true
		}
		if layerEvent {
			e.Attempt = m.Snapshot().Attempt
			e.Layer = l
			if !ok {
				e.Kind = session.Failed
				e.Err = o.notices[l].closed(l)
			}
			if e.Kind != session.Failed && e.Kind != session.Usage {
				return ErrAdapter
			}
			if e.Kind == session.Failed {
				e.Err = o.notices[l].failed(e.Err, l)
				e.Layer = failureLayer(e.Err, l)
				if errors.Is(e.Err, session.ErrStop) || errors.Is(e.Err, session.ErrUncertain) {
					o.recordCleanup(e.Err)
				}
			}
		}
		stop, err = o.step(e)
		if stop || err != nil {
			return err
		}
		// A completed release continuing the owner retires a handled transport
		// cause, or a retryable busy refusal with successful cleanup. Shutdown keeps
		// real failures; the defer above handles a racing busy publication.
		if e.Kind == session.Released && ctx.Err() == nil && (e.Err == nil || errors.Is(e.Err, session.ErrReleaseAbandoned)) && (errors.Is(o.failureErr, session.ErrTransport) || (e.Err == nil && o.handledBusyFailure())) {
			o.failureErr = nil
		}
	}
}

// The worker sends a bounded final outcome before closing done. Drain only
// after joining, so exits never depend on the owner loop or a functioning sink.
func (o *owner) collectCleanup() {
	for len(o.results) > 0 {
		r := <-o.results
		if r.event.Kind == session.Released {
			o.recordCleanup(r.event.Err)
		} else if r.event.Kind == session.Failed {
			// Observe only: the worker has already closed and stopped resources.
			// Replaying Step here would restart policy or release twice.
			if terminalPriority(nil, r.event.Err) != nil {
				o.retainFailure(r.event.Err)
				o.observeResidual(r)
				if observer, ok := o.sink.(Observer); ok {
					snap := o.machine.Snapshot()
					observer.Observe(r.event, snap, snap, core.Instant(time.Since(o.origin)))
				}
			}
		}
	}
}

// observeResidual reports each drained cause on the owner without replaying
// admission or release. The selected primary is observed last through the
// existing aggregate path, preserving its lifecycle and client attribution.
func (o *owner) observeResidual(r completion) {
	var primary *diagnostic.Terminal
	errors.As(r.event.Err, &primary)
	for batch := r.residual(); batch != nil; batch = batch.next {
		for _, err := range batch.outcomes {
			diagnostic.EachTerminal(err, func(terminal *diagnostic.Terminal) {
				if primary.SharesReceipt(terminal) {
					return
				}
				if observer, ok := o.sink.(Observer); ok {
					e := r.event
					e.Err = terminal
					e.Layer = terminal.Origin()
					snap := o.machine.Snapshot()
					observer.Observe(e, snap, snap, core.Instant(time.Since(o.origin)))
				}
			})
		}
	}
}

func (o *owner) handledBusyFailure() bool {
	retryable := (o.cfg.Idle > 0 && o.cfg.LazyBusyWindow > 0) || (o.cfg.Idle == 0 && o.cfg.RetryBusy)
	return o.cleanupErr == nil && retryable && policy(o.failureErr) == session.ErrBusy && failureLayer(o.failureErr, session.Cloud) == session.Cloud
}

func (o *owner) retainFailure(err error) {
	// A busy refusal cannot authorize abandonment of a prior allocation.
	// Preserve the stop obligation independently of per-attempt retirement.
	if errors.Is(err, session.ErrBusy) && (errors.Is(err, session.ErrReleaseAbandoned) || errors.Is(err, session.ErrStop) || errors.Is(err, session.ErrUncertain)) {
		o.recordCleanup(err)
	}
	o.failureErr = terminalPriority(o.failureErr, err)
}
func (o *owner) recordCleanup(err error) {
	if err != nil {
		o.cleanupErr = session.ErrStop
	}
}

func (o *owner) machineStep(e session.Event, now core.Instant) []session.Action {
	actions := o.machine.Step(e, now)
	return actions
}

func (o *owner) step(e session.Event) (bool, error) {
	if e.Kind == session.Failed {
		o.retainFailure(e.Err)
	}
	now := core.Instant(time.Since(o.origin))
	before := o.machine.Snapshot()
	actions := o.machineStep(e, now)
	if observer, ok := o.sink.(Observer); ok {
		observer.Observe(e, before, o.machine.Snapshot(), now)
	}
	for i := 0; i < len(actions); i++ {
		a := actions[i]
		switch a.Kind {
		case session.Dial:
			if o.done != nil {
				return false, ErrAdapter
			}
			o.failureErr = nil
			ctx, cancel := context.WithCancel(context.Background())
			o.cancel = cancel
			o.done = make(chan struct{})
			o.results = make(chan completion, 8)
			go attempt(ctx, a.Attempt, o.cfg, o.factory, o.results, o.done)
		case session.Release:
			o.channels = nil
			o.notices = [session.LayerCount]*eventQueue{}
			if o.cancel != nil {
				o.cancel()
			}
		case session.Admit:
			if o.channels == nil {
				return false, ErrAdapter
			}
			if err := o.channels.TryOpen(a.Client); err != nil {
				// One stuck/full local channel cannot stall or fail siblings.
				// Append timer effects after this batch. Executing them recursively
				// would let the older readiness CancelTimer erase the new idle timer.
				extra := o.machineStep(session.Event{Kind: session.ClientGone, Attempt: a.Attempt, Client: a.Client}, core.Instant(time.Since(o.origin)))
				for _, cleanup := range extra {
					if cleanup.Kind == session.Disconnect {
						o.channels.TryClose(cleanup.Client)
					} else {
						actions = append(actions, cleanup)
					}
				}
				a.Kind = session.Reject
				a.Err = session.ErrBudget
			}
			if err := o.sink.Try(a); err != nil {
				return false, ErrAdapter
			}
		case session.Disconnect:
			if o.channels != nil {
				o.channels.TryClose(a.Client)
			}
			if err := o.sink.Try(a); err != nil {
				return false, ErrAdapter
			}
		case session.Reject:
			if err := o.sink.Try(a); err != nil {
				return false, ErrAdapter
			}
		case session.ArmTimer:
			delay := max(time.Duration(0), time.Duration(a.Deadline)-time.Since(o.origin))
			if o.timer == nil {
				o.timer = time.NewTimer(delay)
			} else {
				o.timer.Reset(delay)
			}
			o.timerC = o.timer.C
		case session.CancelTimer:
			if o.timer != nil {
				o.timer.Stop()
			}
			o.timerC = nil
		case session.Stopped:
			if err := o.sink.Try(a); err != nil {
				return true, errors.Join(ErrAdapter, a.Err)
			}
			return true, a.Err
		}
	}
	o.armObservation()
	return false, nil
}

func (o *owner) armObservation() {
	observer, ok := o.sink.(TimedObserver)
	if !ok {
		return
	}
	deadline := observer.ObservationDeadline()
	if deadline == 0 {
		if o.observationTimer != nil {
			o.observationTimer.Stop()
		}
		o.observationC = nil
		return
	}
	delay := max(time.Duration(0), time.Duration(deadline)-time.Since(o.origin))
	if o.observationTimer == nil {
		o.observationTimer = time.NewTimer(delay)
	} else {
		o.observationTimer.Reset(delay)
	}
	o.observationC = o.observationTimer.C
}
