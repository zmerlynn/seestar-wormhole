package session

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

func stateFixture(t *testing.T, state State) *Machine {
	c := DefaultConfig()
	if state == Dormant {
		c.Idle = time.Second
	}
	m := machine(t, c)
	m.Step(Event{Kind: Start}, 0)
	switch state {
	case Ready:
		ready(m, 0)
	case Releasing:
		m.Step(Event{Kind: Failed, Attempt: 1}, 0)
	case Backoff:
		m.Step(Event{Kind: Failed, Attempt: 1}, 0)
		m.Step(Event{Kind: Released, Attempt: 1}, 0)
	case Busy:
		m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrBusy}, 0)
		m.Step(Event{Kind: Released, Attempt: 1}, 0)
	case Closed:
		m.Step(Event{Kind: Shutdown}, 0)
		m.Step(Event{Kind: Released, Attempt: 1}, 0)
	}
	if m.state != state {
		t.Fatalf("fixture state %v, want %v", m.state, state)
	}
	return m
}
func TestEveryStateEveryEvent(t *testing.T) {
	// Columns enumerate supported Kinds; expected transitions are independent of
	// Step's switch. Tick is delivered after all deadlines. Progress is Cloud.
	kinds := []Kind{Start, Client, ClientGone, Progress, Failed, Released, Usage, Tick, Shutdown}
	wants := map[State][]State{
		Dormant:   {Dormant, Dialing, Dormant, Dormant, Dormant, Dormant, Dormant, Dormant, Closed},
		Dialing:   {Dialing, Dialing, Dialing, Dialing, Releasing, Dialing, Dialing, Releasing, Releasing},
		Ready:     {Ready, Ready, Ready, Ready, Releasing, Ready, Ready, Ready, Releasing},
		Releasing: {Releasing, Releasing, Releasing, Releasing, Releasing, Backoff, Releasing, Releasing, Releasing},
		Backoff:   {Backoff, Backoff, Backoff, Backoff, Backoff, Backoff, Backoff, Dialing, Closed},
		Busy:      {Busy, Busy, Busy, Busy, Busy, Busy, Busy, Busy, Closed},
		Closed:    {Closed, Closed, Closed, Closed, Closed, Closed, Closed, Closed, Closed},
	}
	for _, state := range []State{Dormant, Dialing, Ready, Releasing, Backoff, Busy, Closed} {
		for column, k := range kinds {
			t.Run(fmt.Sprintf("state=%d/event=%d", state, k), func(t *testing.T) {
				m := stateFixture(t, state)
				as := m.Step(Event{Kind: k, Attempt: m.attempt, Client: 1}, core.Instant(time.Minute))
				if m.state != wants[state][column] {
					t.Fatalf("state = %v, want %v; actions %+v", m.state, wants[state][column], as)
				}
				for _, a := range as {
					if a.Kind == Admit && m.state != Ready {
						t.Fatal("admitted without ready session")
					}
				}
			})
		}
	}
}

type harness struct {
	observe                      func(Event, []Action, core.Instant)
	t                            *testing.T
	m                            *Machine
	q                            *sim.Scheduler[Event]
	timer                        *sim.Timer[Event]
	live                         uint64
	releasing                    bool
	allocations, stops, admitted int
	peak                         Limit
}

func newHarness(t *testing.T, c Config, seed uint64) *harness {
	m, err := New(c, rand.New(rand.NewPCG(seed, seed+1)))
	if err != nil {
		t.Fatal(err)
	}
	q := sim.New[Event](0, 128)
	return &harness{t: t, m: m, q: q, timer: sim.NewTimer(q)}
}
func (h *harness) schedule(at core.Instant, e Event) {
	h.t.Helper()
	if err := h.q.Schedule(at, e, 0); err != nil {
		h.t.Fatal(err)
	}
}
func (h *harness) step(e Event, now core.Instant) error {
	h.t.Helper()
	if e.Kind == Released && e.Attempt == h.live && h.releasing {
		h.live = 0
		h.releasing = false
	}
	actions := h.m.Step(e, now)
	if h.observe != nil {
		h.observe(e, actions, now)
	}
	for _, a := range actions {
		switch a.Kind {
		case Dial:
			if h.live != 0 {
				h.t.Fatalf("overlapping allocation: old %d new %d", h.live, a.Attempt)
			}
			h.live = a.Attempt
			h.allocations++
			for l := Cloud; l < LayerCount; l++ {
				h.schedule(now+core.Instant(l+1), Event{Kind: Progress, Attempt: a.Attempt, Layer: l})
			}
		case Release:
			if h.live != a.Attempt || h.releasing {
				h.t.Fatalf("duplicate/missing stop: live %d action %+v", h.live, a)
			}
			h.stops++
			h.releasing = true
			h.schedule(now+1, Event{Kind: Released, Attempt: a.Attempt})
		case Admit:
			if h.m.Snapshot().State != Ready || a.Attempt != h.live || h.releasing {
				h.t.Fatal("client admitted without ready allocation")
			}
			h.admitted++
		case ArmTimer:
			if err := h.timer.Arm(a.Deadline, Event{Kind: Tick}); err != nil {
				h.t.Fatal(err)
			}
		case CancelTimer:
			h.timer.Cancel()
		}
	}
	s := h.m.Snapshot()
	if s.Clients < 0 || s.Clients > h.m.cfg.MaxClients || s.Queued < 0 || s.Queued > h.m.cfg.MaxQueued || s.Queued > s.Clients {
		h.t.Fatalf("invalid clients: %+v", s)
	}
	if s.Usage.Bytes > h.m.cfg.Budgets.Total.Bytes || s.Usage.Count > h.m.cfg.Budgets.Total.Count {
		h.t.Fatalf("budget exceeded: %+v", s)
	}
	h.peak.Bytes = max(h.peak.Bytes, s.Usage.Bytes)
	h.peak.Count = max(h.peak.Count, s.Usage.Count)
	return nil
}
func (h *harness) run(until core.Instant) {
	h.t.Helper()
	if err := h.q.Run(until, 10000, h.step); err != nil {
		h.t.Fatal(err)
	}
}
func (h *harness) send(e Event) { h.t.Helper(); h.schedule(h.q.Now(), e); h.run(h.q.Now()) }
func (h *harness) finish() {
	h.t.Helper()
	h.send(Event{Kind: Shutdown})
	h.run(h.q.Now() + 10)
	if h.live != 0 || h.allocations != h.stops || h.m.Snapshot().Clients != 0 {
		h.t.Fatalf("cleanup: live=%d allocations=%d stops=%d snapshot=%+v", h.live, h.allocations, h.stops, h.m.Snapshot())
	}
}

func TestEightHourBudgetAndMemoryPlateau(t *testing.T) {
	c := DefaultConfig()
	c.Idle = 2 * time.Second
	h := newHarness(t, c, 42)
	h.send(Event{Kind: Start})
	var baseline uint64
	var firstPeak Limit
	for minute := 0; minute < 480; minute++ {
		at := core.Instant(time.Duration(minute) * time.Minute)
		h.run(at)
		h.send(Event{Kind: Client, Client: uint64(minute)})
		h.run(at + 10)
		if minute%7 == 0 {
			h.send(Event{Kind: Failed, Attempt: h.m.attempt})
		} else {
			// A slow channel retains its full reservation while siblings remain usable.
			h.send(Event{Kind: Client, Client: uint64(minute) + 1000})
			h.send(Event{Kind: Usage, Attempt: h.m.attempt, Layer: Tunnel, Bytes: 4096, Count: 2})
			h.send(Event{Kind: ClientGone, Attempt: h.m.attempt, Client: uint64(minute) + 1000})
			h.send(Event{Kind: ClientGone, Attempt: h.m.attempt, Client: uint64(minute)})
		}
		h.run(at + core.Instant(10*time.Second))
		if s := h.m.Snapshot(); s.State != Dormant || s.Usage != (Limit{}) || s.Clients != 0 {
			t.Fatalf("minute %d retained state: %+v", minute, s)
		}
		if _, count := h.q.Usage(); count != 0 {
			t.Fatalf("minute %d retained %d simulator events", minute, count)
		}
		if minute == 239 {
			runtime.GC()
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			baseline = stats.HeapAlloc
			firstPeak = h.peak
		}
	}
	h.run(core.Instant(8 * time.Hour))
	h.finish()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	if stats.HeapAlloc > baseline+1<<20 {
		t.Fatalf("heap grew across second four hours: %d -> %d", baseline, stats.HeapAlloc)
	}
	if h.peak != firstPeak {
		t.Fatalf("budget peak grew: %+v -> %+v", firstPeak, h.peak)
	}
	if h.allocations != 480 || h.admitted < 480 {
		t.Fatalf("soak activity allocations=%d admitted=%d", h.allocations, h.admitted)
	}
	t.Logf("8h: allocations/stops=%d/%d, admissions=%d, peak=%+v, heap midpoint/end=%d/%d, scheduler peak=%d", h.allocations, h.stops, h.admitted, h.peak, baseline, stats.HeapAlloc, h.q.PeakCount)
}
