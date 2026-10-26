package session

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

// errPanicOutcome models the sanitized terminal outcome delivered by the edge.
// TestSeededConstructionFaults exercises actual panicking callbacks and handoff.
var errPanicOutcome = errors.New("session edge panic")

// The environment owns independently scheduled construction and cleanup. Its
// oracle uses observable actions/events, never private Machine policy fields.
type lifecycle struct {
	t                                         *testing.T
	m                                         *Machine
	c                                         Config
	r                                         *rand.Rand
	q                                         *sim.Scheduler[Event]
	timer                                     *sim.Timer[Event]
	live                                      uint64
	phase                                     Layer
	stopping, blocked, uncertain, retry, fair bool
	earliest                                  core.Instant
	recentUntil                               core.Instant
	releasedAttempt                           uint64
	refusalRetry                              bool
	recentRefusals                            int
	cap                                       time.Duration
	stops                                     map[uint64]int
	admitted                                  map[uint64]bool
	commands                                  [Closed + 1]int
	failures                                  [LayerCount + 1][6]int
	stopFailures                              [LayerCount + 1]int
	timeouts                                  [LayerCount]int
	allocations, admissions, releases         int
	queuedTerminal                            [3]int
}

func newLifecycle(t *testing.T, seed uint64, lazy bool) *lifecycle {
	c := DefaultConfig()
	c.DialTimeout = 50 * time.Millisecond
	c.StopTimeout = 40 * time.Millisecond
	c.BackoffMin = 20 * time.Millisecond
	c.BackoffMax = 160 * time.Millisecond
	if lazy {
		c.Idle = 100 * time.Millisecond
	}
	m, err := New(c, fixedRNG(0))
	if err != nil {
		t.Fatal(err)
	}
	q := sim.New[Event](0, 256)
	return &lifecycle{t: t, m: m, c: c, r: rand.New(rand.NewPCG(seed, 99)), q: q, timer: sim.NewTimer(q), cap: 2 * c.BackoffMin, stops: map[uint64]int{}, admitted: map[uint64]bool{}}
}
func (h *lifecycle) schedule(at core.Instant, e Event) {
	h.t.Helper()
	if err := h.q.Schedule(at, e, 0); err != nil {
		h.t.Fatal(err)
	}
}
func (h *lifecycle) delay(n uint64) core.Instant {
	return core.Instant((1 + h.r.Uint64()%n) * uint64(time.Millisecond))
}
func (h *lifecycle) step(e Event, now core.Instant) error {
	h.t.Helper()
	before := h.m.Snapshot()
	current := e.Attempt == before.Attempt && e.Attempt != 0
	if e.Kind == Client || e.Kind == ClientGone || e.Kind == Shutdown {
		h.commands[before.State]++
	}
	recentRefusal := errors.Is(e.Err, ErrBusy) && h.releasedAttempt != 0 && h.releasedAttempt < e.Attempt && now < h.recentUntil && !h.blocked && !h.uncertain
	if current && recentRefusal {
		h.recentRefusals++
		if e.Kind == Failed {
			h.refusalRetry = true
		}
	}
	if current && e.Kind == Failed && before.State != Closed {
		cause := 0
		switch {
		case errors.Is(e.Err, errPanicOutcome):
			cause = 5
		case errors.Is(e.Err, ErrStop):
			cause = 2
		case errors.Is(e.Err, ErrUncertain):
			cause = 4
		case errors.Is(e.Err, ErrBusy):
			cause = 1
		case errors.Is(e.Err, ErrTimeout):
			cause = 3
		}
		h.failures[h.phase][cause]++
		switch {
		case errors.Is(e.Err, ErrStop) || errors.Is(e.Err, ErrUncertain):
			h.uncertain = true
		case errors.Is(e.Err, ErrBusy) && !recentRefusal:
			h.blocked = true
		}
	}
	if (e.Kind == Failed && current && (!errors.Is(e.Err, ErrBusy) || recentRefusal) && !errors.Is(e.Err, ErrStop) && !errors.Is(e.Err, ErrUncertain) && (before.State == Dialing || before.State == Ready)) ||
		(e.Kind == Tick && before.State == Dialing && now >= before.Deadline) {
		h.retry = true
		if e.Kind == Tick {
			h.timeouts[h.phase]++
		}
	}
	if current && before.State == Dialing && e.Kind == Progress && !h.stopping && e.Layer == h.phase {
		h.phase++
		if h.phase == LayerCount {
			h.cap = 2 * h.c.BackoffMin
			h.refusalRetry = false
		}
	}
	if current && e.Kind == Released && h.stopping {
		if e.Err == nil && e.ReleasedAllocation {
			h.recentUntil = now + core.Instant(h.c.RecentReleaseWindow)
			h.releasedAttempt = e.Attempt
		}
		h.releases++
		h.live = 0
		h.stopping = false
		if e.Err != nil {
			h.stopFailures[h.phase]++
			h.uncertain = true
		}
		if h.retry {
			h.earliest = now + core.Instant(max(h.c.BackoffMin, (h.cap+1)/2))
			h.cap = min(h.c.BackoffMax, 2*h.cap)
			h.retry = false
		}
	}
	for _, a := range h.m.Step(e, now) {
		switch a.Kind {
		case Dial:
			if h.refusalRetry && now >= h.recentUntil {
				h.t.Fatalf("refusal retry at %v reached exclusive horizon %v", now, h.recentUntil)
			}
			if h.live != 0 || h.blocked || h.uncertain || now < h.earliest {
				h.t.Fatalf("unsafe dial at %v: live=%d busy=%v uncertain=%v retry-not-before=%v event=%+v", now, h.live, h.blocked, h.uncertain, h.earliest, e)
			}
			h.live = a.Attempt
			h.phase = Cloud
			h.allocations++
			at := now
			failed := int(LayerCount) + 1
			cause := 0
			if !h.fair {
				failed = h.r.IntN(int(LayerCount) + 2)
				cause = h.r.IntN(6)
			}
			for l := Cloud; l < LayerCount; l++ {
				at += h.delay(15)
				if h.fair {
					at = now + core.Instant(l+1)*core.Instant(time.Millisecond)
				}
				if int(l) == failed {
					errs := []error{ErrTransport, ErrBusy, ErrStop, ErrTimeout, ErrUncertain, errors.Join(errPanicOutcome, ErrUncertain)}
					if cause == 3 {
						at = now + core.Instant(h.c.DialTimeout) + h.delay(10)
					}
					h.schedule(at, Event{Kind: Failed, Attempt: a.Attempt, Err: h.joined(errs[cause])})
					break
				}
				h.schedule(at, Event{Kind: Progress, Attempt: a.Attempt, Layer: l})
				if !h.fair { // Independently delayed duplicate/out-of-order and stale completions.
					h.schedule(now+h.delay(80), Event{Kind: Progress, Attempt: a.Attempt, Layer: l})
					h.schedule(now+h.delay(80), Event{Kind: Failed, Attempt: a.Attempt - 1, Err: ErrBusy})
				}
			}
			if failed == int(LayerCount) {
				errs := []error{ErrTransport, ErrBusy, ErrStop, ErrTimeout, ErrUncertain, errors.Join(errPanicOutcome, ErrUncertain)}
				h.schedule(at+h.delay(15), Event{Kind: Failed, Attempt: a.Attempt, Err: h.joined(errs[cause])})
			}
		case Release:
			if before.State == Ready {
				h.recentUntil = now + core.Instant(h.c.RecentReleaseWindow)
				h.releasedAttempt = a.Attempt
			}
			h.stops[a.Attempt]++
			if a.Attempt != h.live || h.stops[a.Attempt] != 1 {
				h.t.Fatalf("stop ownership: %+v live=%d stops=%v", a, h.live, h.stops)
			}
			h.stopping = true
			delay := h.delay(55)
			var err error
			if !h.fair && h.r.IntN(4) == 0 {
				err = ErrStop
			}
			if h.fair {
				delay = core.Instant(time.Millisecond)
			}
			h.schedule(now+delay, Event{Kind: Released, Attempt: a.Attempt, Err: err, ReleasedAllocation: h.phase > Cloud})
			if !h.fair {
				// Model a terminal notice queued behind the release request,
				// independently of the worker completion that triggered it.
				k := h.r.IntN(3)
				h.queuedTerminal[k]++
				err := []error{ErrBusy, ErrStop, ErrUncertain}[k]
				h.schedule(now+delay/2, Event{Kind: Failed, Attempt: a.Attempt, Err: h.joined(err)})
				h.schedule(now+delay+h.delay(30), Event{Kind: Released, Attempt: a.Attempt, Err: err, ReleasedAllocation: h.phase > Cloud})
			}
		case Admit:
			if h.phase != LayerCount || h.live != a.Attempt || h.stopping || h.m.Snapshot().State != Ready {
				h.t.Fatalf("unsafe admission: %+v phase=%v", a, h.phase)
			}
			h.admitted[a.Client] = true
			h.admissions++
		case Reject, Disconnect:
			delete(h.admitted, a.Client)
		case ArmTimer:
			if err := h.timer.Arm(a.Deadline, Event{Kind: Tick}); err != nil {
				h.t.Fatal(err)
			}
		case CancelTimer:
			h.timer.Cancel()
		}
	}
	s := h.m.Snapshot()
	if s.Clients < 0 || s.Clients > h.c.MaxClients || s.Queued < 0 || s.Queued > h.c.MaxQueued || s.Queued > s.Clients || s.Usage.Bytes > h.c.Budgets.Total.Bytes || s.Usage.Count > h.c.Budgets.Total.Count {
		h.t.Fatalf("accounting: %+v", s)
	}
	return nil
}
func (h *lifecycle) joined(err error) error {
	if h.r.IntN(2) == 0 {
		other := errors.New("diagnostic")
		if errors.Is(err, ErrStop) || errors.Is(err, ErrUncertain) {
			other = ErrBusy
		}
		return errors.Join(fmt.Errorf("wrapped: %w", err), other)
	}
	return err
}
func (h *lifecycle) run(d core.Instant) {
	h.t.Helper()
	if err := h.q.Run(h.q.Now()+d, 10000, h.step); err != nil {
		h.t.Fatal(err)
	}
}
func (h *lifecycle) send(e Event) {
	h.t.Helper()
	if err := h.step(e, h.q.Now()); err != nil {
		h.t.Fatal(err)
	}
}
func (h *lifecycle) finish() {
	h.t.Helper()
	h.send(Event{Kind: Shutdown})
	h.run(core.Instant(time.Second))
	if h.live != 0 || len(h.stops) != h.allocations || h.m.Snapshot().Clients != 0 {
		h.t.Fatalf("leaked allocation: live=%d stops=%v allocations=%d", h.live, h.stops, h.allocations)
	}
}

func TestSeededLifecycleProperties(t *testing.T) {
	var recentRefusals int
	var commands [Closed + 1]int
	var failures [LayerCount + 1][6]int
	var stopFailures [LayerCount + 1]int
	var timeouts [LayerCount]int
	var queuedTerminal [3]int
	for seed := uint64(1); seed <= 20; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			// Exhaust every construction boundary instead of hoping random commands
			// land there. The composed account property expands Cloud into login,
			// profile, detail and allocation request/body barriers.
			for phase := Cloud; phase <= LayerCount; phase++ {
				h := newLifecycle(t, seed, false)
				h.fair = true
				h.send(Event{Kind: Start})
				h.run(core.Instant(phase) * core.Instant(time.Millisecond))
				if h.phase != phase {
					t.Fatalf("phase=%v, want %v", h.phase, phase)
				}
				h.send(Event{Kind: Shutdown})
				h.run(core.Instant(10 * time.Millisecond))
				if h.m.Snapshot().State != Closed || h.live != 0 {
					t.Fatal("shutdown retained construction")
				}
			}

			// Retain minimal traces that reproduce the ordering failures.
			if seed == 1 {
				TestBusyWinsOverTimeout(t)
			}
			if seed == 2 {
				TestLazyFailuresRespectBackoff(t)
			}
			// Both public uncertainty sentinels must independently delay retry,
			// including joined errors, cleanup and late failure.
			for _, terminal := range []error{ErrStop, ErrUncertain} {
				for _, order := range []string{"failure", "releasing", "released"} {
					h := newLifecycle(t, seed, false)
					h.fair = true
					h.send(Event{Kind: Start})
					if order != "failure" {
						h.send(Event{Kind: Failed, Attempt: 1, Err: ErrTransport})
					}
					if order == "released" {
						h.run(core.Instant(2 * time.Millisecond))
					}
					h.send(Event{Kind: Failed, Attempt: 1, Err: h.joined(terminal)})
					h.run(core.Instant(time.Second))
					if h.allocations != 1 {
						t.Fatalf("%s uncertainty retried", order)
					}
					h.send(Event{Kind: Client, Client: 998})
					h.run(core.Instant(90 * time.Millisecond))
					if h.admitted[998] || h.allocations != 1 {
						t.Fatalf("%s uncertainty admitted demand", order)
					}
					h.finish()
				}
			}
			// Fair-success suffixes must recover after transport failure.
			// Waiting demand is submitted after deliberate drops.
			for _, lazy := range []bool{false, true} {
				for _, mode := range []string{"retry", "ready-arrival"} {
					h := newLifecycle(t, seed, lazy)
					h.fair = true
					h.send(Event{Kind: Start})
					h.send(Event{Kind: Client, Client: 998})
					h.run(core.Instant(10 * time.Millisecond))
					switch mode {
					case "retry":
						h.send(Event{Kind: Failed, Attempt: h.live, Err: ErrTransport})
						h.run(core.Instant(2 * time.Millisecond))

					}
					h.send(Event{Kind: Client, Client: 999})
					h.run(core.Instant(time.Second))
					if !h.admitted[999] {
						t.Fatalf("%s lazy=%v: waiting client never admitted: %+v", mode, lazy, h.m.Snapshot())
					}
					h.finish()
				}
			}
			// An idle release authorizes a bounded own-session refusal retry.
			h := newLifecycle(t, seed, true)
			h.fair = true
			h.send(Event{Kind: Start})
			h.send(Event{Kind: Client, Client: 1})
			h.run(core.Instant(10 * time.Millisecond))
			h.send(Event{Kind: ClientGone, Client: 1})
			h.run(core.Instant(110 * time.Millisecond))
			h.send(Event{Kind: Client, Client: 2})
			h.send(Event{Kind: Failed, Attempt: h.live, Err: ErrBusy})
			h.run(core.Instant(90 * time.Millisecond))
			if !h.admitted[2] {
				t.Fatal("own-session refusal lost demand")
			}
			recentRefusals += h.recentRefusals
			h.finish()

			for episode := 0; episode < 60; episode++ {
				h := newLifecycle(t, seed*100+uint64(episode), seed%2 == 0)
				h.send(Event{Kind: Start})
				for i := 0; i < 100 && h.m.Snapshot().State != Closed; i++ {
					h.run(h.delay(8))
					e := Event{Client: uint64(i + 1), Attempt: h.m.Snapshot().Attempt}
					switch h.r.IntN(10) {
					case 0, 1, 2, 3:
						e.Kind = Client
					case 4:
						e.Kind = ClientGone
						e.Client = uint64(h.r.IntN(i+1) + 1)
					case 5:
						e.Kind = Tick
					case 6:
						e.Kind = ClientGone
					case 7:
						e.Kind = Failed
						e.Err = ErrTransport
					case 8:
						e.Kind = Failed
						e.Err = h.joined([]error{ErrBusy, ErrStop, ErrUncertain}[h.r.IntN(3)])
					case 9:
						e.Kind = Progress
						e.Layer = Layer(h.r.IntN(int(LayerCount)))
					}
					// Random progress is a duplicate of an already completed phase only.
					if e.Kind != Progress || e.Layer < h.phase {
						h.send(e)
					}
				}
				// Switch this same random trace to fair successful completions. Once
				// pending cleanup has concluded, retryable histories must recover;
				// terminal busy and ownership delays must continue to reject demand.
				h.fair = true
				h.run(core.Instant(time.Second))
				if h.m.Snapshot().State != Closed {
					h.send(Event{Kind: Client, Client: 99999})
					h.run(core.Instant(90 * time.Millisecond))
					if h.blocked || h.uncertain {
						if h.admitted[99999] {
							t.Fatal("terminal history admitted demand")
						}
					} else if !h.admitted[99999] {
						t.Fatalf("retryable random trace stranded demand: %+v", h.m.Snapshot())
					}
				}
				h.finish()
				recentRefusals += h.recentRefusals
				for i, n := range h.commands {
					commands[i] += n
				}
				for k, n := range h.queuedTerminal {
					queuedTerminal[k] += n
				}
				for l, n := range h.timeouts {
					timeouts[l] += n
				}
				for l, n := range h.stopFailures {
					stopFailures[l] += n
				}
				for l := range h.failures {
					for k, n := range h.failures[l] {
						failures[l][k] += n
					}
				}
			}

		})
	}
	if recentRefusals == 0 {
		t.Fatal("no recent-release refusal coverage")
	}
	t.Logf("recent-release refusals=%d", recentRefusals)
	if commands[Dialing] == 0 || commands[Releasing] == 0 {
		t.Fatalf("vacuous command interleaving coverage: %v", commands)
	}
	for l := range failures {
		for k, n := range failures[l] {
			if n == 0 {
				t.Errorf("missing failure phase=%d cause=%d", l, k)
			}
		}
	}
	for l, n := range stopFailures {
		if n == 0 {
			t.Errorf("missing release failure at phase %d", l)
		}
	}
	for l, n := range timeouts {
		if n == 0 {
			t.Errorf("missing dial timeout at phase %d", l)
		}
	}
	for k, n := range queuedTerminal {
		if n == 0 {
			t.Errorf("missing queued terminal cause=%d", k)
		}
	}
	t.Logf("terminal notices queued behind release [busy stop uncertain]=%v", queuedTerminal)
	t.Logf("dial timeouts by phase=%v", timeouts)
	t.Logf("release failures by phase=%v", stopFailures)
	t.Logf("commands by state=%v; failures by phase [transport busy stop timeout uncertain panic]=%v", commands, failures)
}

// Exercise the oracle's upper bound with public events and an overdue timer.
func TestLifecycleRefusalUpperBound(t *testing.T) {
	h := newLifecycle(t, 1, true)
	h.fair = true
	step := func(e Event, at time.Duration) {
		t.Helper()
		if err := h.step(e, core.Instant(at)); err != nil {
			t.Fatal(err)
		}
	}
	step(Event{Kind: Start}, 0)
	step(Event{Kind: Client, Client: 1}, 0)
	for l := Cloud; l < LayerCount; l++ {
		step(Event{Kind: Progress, Attempt: 1, Layer: l}, 0)
	}
	step(Event{Kind: ClientGone, Client: 1}, 0)
	step(Event{Kind: Tick}, time.Second)
	step(Event{Kind: Released, Attempt: 1, ReleasedAllocation: true}, time.Second)
	end := time.Second + h.c.RecentReleaseWindow
	step(Event{Kind: Client, Client: 2}, end-time.Millisecond)
	step(Event{Kind: Failed, Attempt: 2, Err: ErrBusy}, end-time.Millisecond)
	step(Event{Kind: Released, Attempt: 2}, end-time.Millisecond)
	step(Event{Kind: Tick}, end+time.Hour)
	if h.m.Snapshot().State != Busy {
		t.Fatal("expired refusal did not become busy")
	}
}
