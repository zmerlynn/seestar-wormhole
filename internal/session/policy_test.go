package session

import (
	"errors"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestFailureAtEveryPipelinePhase(t *testing.T) {
	for failed := Cloud; failed <= LayerCount; failed++ {
		for _, cause := range []error{ErrTransport, errors.Join(ErrBusy, errors.New("adapter detail"))} {
			m := machine(t, DefaultConfig())
			m.Step(Event{Kind: Start}, 0)
			m.Step(Event{Kind: Client, Client: 1}, 0)
			for l := Cloud; l < failed; l++ {
				as := m.Step(Event{Kind: Progress, Attempt: 1, Layer: l}, 0)
				if l < Tunnel && has(as, Admit) {
					t.Fatal("admitted before tunnel readiness")
				}
			}
			as := m.Step(Event{Kind: Failed, Attempt: 1, Layer: failed, Err: cause}, 1)
			if !has(as, Release) {
				t.Fatalf("phase %v failed without release", failed)
			}
			if has(m.Step(Event{Kind: Failed, Attempt: 1, Err: cause}, 2), Release) {
				t.Fatal("duplicate failure stopped twice")
			}
			m.Step(Event{Kind: Released, Attempt: 1}, 3)
			want := Backoff
			if errors.Is(cause, ErrBusy) {
				want = Busy
			}
			if m.state != want {
				t.Fatalf("failure phase %v state = %v, want %v", failed, m.state, want)
			}
		}
	}
}
func TestBackoffEndpointsAndCap(t *testing.T) {
	for _, upper := range []bool{false, true} {
		c := DefaultConfig()
		c.BackoffMin = 3 * time.Nanosecond
		c.BackoffMax = 31 * time.Nanosecond
		m := machine(t, c)
		m.Step(Event{Kind: Start}, 0)
		var now core.Instant
		cap := min(c.BackoffMax, 2*c.BackoffMin)
		for i := 0; i < 20; i++ {
			lo := max(c.BackoffMin, (cap+1)/2)
			want := lo
			m.rng = fixedRNG(0)
			if upper {
				m.rng = fixedRNG(cap - lo)
				want = cap
			}
			m.Step(Event{Kind: Failed, Attempt: m.attempt}, now)
			m.Step(Event{Kind: Released, Attempt: m.attempt}, now)
			got := m.deadline.Sub(now)
			if got != want || got < c.BackoffMin || got > c.BackoffMax {
				t.Fatalf("retry %d upper=%v delay=%v want=%v", i, upper, got, want)
			}
			attempt := m.attempt
			m.Step(Event{Kind: Tick}, m.deadline-1)
			if m.attempt != attempt {
				t.Fatal("retry before deadline")
			}
			now = m.deadline
			m.Step(Event{Kind: Tick}, now)
			if m.attempt != attempt+1 {
				t.Fatal("retry missing at deadline")
			}
			cap = min(c.BackoffMax, cap*2)
		}
	}
}
func TestReleaseFailureAndTimeoutDoNotRetry(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		m := machine(t, DefaultConfig())
		m.Step(Event{Kind: Start}, 0)
		m.Step(Event{Kind: Failed, Attempt: 1}, 1)
		if timeout {
			m.Step(Event{Kind: Tick}, m.deadline)
		} else {
			m.Step(Event{Kind: Released, Attempt: 1, Err: errors.New("stop failed")}, 2)
		}
		as := m.Step(Event{Kind: Tick}, core.Instant(time.Hour))
		if has(as, Dial) || m.attempt != 1 {
			t.Fatal("uncertain release automatically retried")
		}
		if timeout && m.state != Closed {
			t.Fatal("release deadline did not close local owner")
		}
		if !timeout {
			as = m.Step(Event{Kind: Client, Client: 1}, core.Instant(time.Hour))
			if len(as) != 1 || as[0].Err != ErrStop {
				t.Fatalf("stop failure not surfaced: %+v", as)
			}
		}
	}
}
func TestIdleClockRegressionAndClientReturn(t *testing.T) {
	c := DefaultConfig()
	c.Idle = time.Second
	m := machine(t, c)
	m.Step(Event{Kind: Start}, 0)
	m.Step(Event{Kind: Client, Client: 1}, 0)
	ready(m, 0)
	m.Step(Event{Kind: ClientGone, Client: 1, Attempt: 1}, 100)
	deadline := m.deadline
	m.Step(Event{Kind: Tick}, 1)
	if m.deadline != deadline {
		t.Fatal("regressing clock changed deadline")
	}
	m.Step(Event{Kind: Client, Client: 2}, deadline-1)
	if has(m.Step(Event{Kind: Tick}, deadline), Release) {
		t.Fatal("stale idle timer released active client")
	}
}
func TestLayerBudgetsAndOutOfOrderReadiness(t *testing.T) {
	for l := Cloud; l < LayerCount; l++ {
		for _, counts := range []bool{false, true} {
			m := machine(t, DefaultConfig())
			m.Step(Event{Kind: Start}, 0)
			m.Step(Event{Kind: Progress, Attempt: 1, Layer: Tunnel}, 0)
			if m.state == Ready {
				t.Fatal("out of order readiness admitted session")
			}
			limit := m.cfg.Budgets.Layers[l]
			e := Event{Kind: Usage, Attempt: 1, Layer: l, Bytes: limit.Bytes, Count: limit.Count}
			if has(m.Step(e, 0), Release) {
				t.Fatal("equality rejected")
			}
			if counts {
				e.Count++
			} else {
				e.Bytes++
			}
			if !has(m.Step(e, 0), Release) {
				t.Fatalf("layer %v counts=%v exceeded budget without release", l, counts)
			}
		}
	}
}

func TestFirstTransportRetryIsJittered(t *testing.T) {
	var deadlines [2]core.Instant
	for i := range deadlines {
		m := machine(t, DefaultConfig())
		m.rng = fixedRNG(i)
		m.Step(Event{Kind: Start}, 0)
		ready(m, 0)
		m.Step(Event{Kind: Failed, Attempt: 1}, 0)
		m.Step(Event{Kind: Released, Attempt: 1}, 0)
		deadlines[i] = m.deadline
	}
	if deadlines[0] == deadlines[1] {
		t.Fatalf("transport retry ignored injected jitter: %v", deadlines)
	}
}

// Local policy permits refusal retries within the own-session window.
func TestRefusalAfterOwnRelease(t *testing.T) {
	c := DefaultConfig()
	m := machine(t, c)
	m.Step(Event{Kind: Start}, 0)
	for l := Cloud; l < LayerCount; l++ {
		m.Step(Event{Kind: Progress, Attempt: 1, Layer: l}, 1)
	}
	m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrTransport}, 2)
	m.Step(Event{Kind: Released, Attempt: 1, ReleasedAllocation: true}, 3)
	now := m.deadline
	m.Step(Event{Kind: Tick}, now)
	now++
	m.Step(Event{Kind: Failed, Attempt: 2, Err: ErrBusy}, now)
	m.Step(Event{Kind: Released, Attempt: 2}, now+1)
	if m.Snapshot().State != Backoff {
		t.Fatalf("recent refusal: %+v", m.Snapshot())
	}
}

func TestRefusalWindowDoesNotSlide(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		c := DefaultConfig()
		m := machine(t, c)
		m.Step(Event{Kind: Start}, 0)
		m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrTransport}, 1)
		m.Step(Event{Kind: Released, Attempt: 1, ReleasedAllocation: confirmed}, 2)
		now := m.deadline
		m.Step(Event{Kind: Tick}, now)
		now++
		for attempt := uint64(2); attempt < 20; attempt++ {
			m.Step(Event{Kind: Failed, Attempt: attempt, Err: ErrBusy}, now)
			m.Step(Event{Kind: Released, Attempt: attempt}, now+1)
			if !confirmed || now >= 2+core.Instant(c.RecentReleaseWindow) {
				if m.Snapshot().State != Busy {
					t.Fatalf("expired/unowned refusal: %+v", m.Snapshot())
				}
				if has(m.Step(Event{Kind: Tick}, now+core.Instant(time.Hour)), Dial) {
					t.Fatal("busy reacquired")
				}
				break
			}
			if m.Snapshot().State != Backoff {
				t.Fatalf("recent refusal did not back off: %+v", m.Snapshot())
			}
			deadline := m.Snapshot().Deadline
			if deadline <= now+1 || deadline > now+1+core.Instant(c.BackoffMax) {
				t.Fatal("unbounded retry")
			}
			if has(m.Step(Event{Kind: Tick}, deadline-1), Dial) {
				t.Fatal("early retry")
			}
			actions := m.Step(Event{Kind: Tick}, deadline)
			if deadline >= 2+core.Instant(c.RecentReleaseWindow) {
				if has(actions, Dial) || m.Snapshot().State != Busy {
					t.Fatalf("expired queued retry: %+v", m.Snapshot())
				}
				break
			}
			if !has(actions, Dial) {
				t.Fatal("missing retry")
			}
			now = deadline + 1
			if attempt == 19 {
				t.Fatal("refusals kept extending release window")
			}
		}
	}
}
