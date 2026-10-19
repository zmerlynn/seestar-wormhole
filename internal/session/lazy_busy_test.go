package session

import (
	"errors"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func lazyBusyMachine(t *testing.T) *Machine {
	c := DefaultConfig()
	c.Idle = time.Minute
	c.LazyBusyWindow = LazyBusyWindow
	m := machine(t, c)
	m.Step(Event{Kind: Start}, 0)
	m.Step(Event{Kind: Client, Client: 1}, 0)
	return m
}

func TestLazyBusyCoreBudgetAndBackoff(t *testing.T) {
	m := lazyBusyMachine(t)
	now := core.Instant(0)
	var gaps []time.Duration
	for m.state != Dormant {
		as := m.Step(Event{Kind: Failed, Attempt: m.attempt, Layer: Cloud, Err: ErrBusy}, now)
		if has(as, Reject) {
			t.Fatalf("busy rejected queued client at %v", now)
		}
		m.Step(Event{Kind: Released, Attempt: m.attempt}, now)
		before := m.Snapshot()
		if before.Queued != 1 || before.State != Backoff {
			t.Fatalf("queued backoff=%+v", before)
		}
		gaps = append(gaps, time.Duration(before.Deadline-now))
		now = before.Deadline
		as = m.Step(Event{Kind: Tick}, now)
		if now >= core.Instant(90*time.Second) {
			if !has(as, Reject) || has(as, Dial) {
				t.Fatalf("budget actions=%+v", as)
			}
			break
		}
	}
	if len(gaps) < 4 || gaps[0] != 2*time.Second || gaps[1] != 4*time.Second || gaps[2] != 7500*time.Millisecond {
		t.Fatalf("jitter lower bounds=%v", gaps)
	}
	for _, gap := range gaps {
		if gap > 15*time.Second {
			t.Fatalf("backoff=%v exceeds 15s", gap)
		}
	}
	if m.Snapshot().State != Dormant || m.Snapshot().Queued != 0 {
		t.Fatalf("exhaustion=%+v", m.Snapshot())
	}
	if !has(m.Step(Event{Kind: Client, Client: 2}, now), Dial) {
		t.Fatal("fresh request blocked")
	}
}

func TestLazyBusyCoreLateTimerAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		m := lazyBusyMachine(t)
		m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrBusy}, 1)
		m.Step(Event{Kind: Released, Attempt: 1}, 2)
		if cancel {
			as := m.Step(Event{Kind: ClientGone, Client: 1}, 3)
			if !has(as, CancelTimer) || m.state != Dormant {
				t.Fatalf("cancel=%+v %+v", as, m.Snapshot())
			}
		}
		as := m.Step(Event{Kind: Tick}, core.Instant(time.Hour))
		if has(as, Dial) || m.state != Dormant {
			t.Fatalf("delayed timer dialed: %+v %+v", as, m.Snapshot())
		}
	}
}

func TestLazyBusyCoreFailureAndCleanupBlock(t *testing.T) {
	for _, cause := range []error{ErrUncertain, ErrStop, errors.Join(ErrBusy, ErrUncertain)} {
		m := lazyBusyMachine(t)
		m.Step(Event{Kind: Failed, Attempt: 1, Err: cause}, 1)
		m.Step(Event{Kind: Released, Attempt: 1, Err: ErrStop}, 2)
		as := m.Step(Event{Kind: Client, Client: 2}, 3)
		if !has(as, Reject) || has(as, Dial) {
			t.Fatalf("failure %v allowed dial: %+v", cause, as)
		}
	}
	m := lazyBusyMachine(t)
	as := m.Step(Event{Kind: Failed, Attempt: 1, Layer: DTLS, Err: ErrBusy}, 0)
	if !has(as, Reject) {
		t.Fatalf("non-cloud busy retained demand: %+v", as)
	}
	m.Step(Event{Kind: Released, Attempt: 1}, 0)
	// Deliver the forbidden retry's first Tick while its budget is still live.
	if as := m.Step(Event{Kind: Tick}, core.Instant(2*time.Second)); has(as, Dial) {
		t.Fatalf("retried non-cloud busy: %+v", as)
	}
}

func TestLazyBusyCoreCanceledDialLateBusyAndFreshDemand(t *testing.T) {
	m := lazyBusyMachine(t)
	as := m.Step(Event{Kind: ClientGone, Client: 1}, 1)
	if !has(as, Release) {
		t.Fatal("last client did not cancel attempt")
	}
	as = m.Step(Event{Kind: Client, Client: 2}, 2)
	m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 3)
	if has(as, Reject) || has(as, Dial) {
		t.Fatalf("new demand during cleanup=%+v", as)
	}
	as = m.Step(Event{Kind: Released, Attempt: 1}, 4)
	if has(as, Dial) {
		t.Fatal("joined cancellation bypassed minimum dial interval")
	}
	as = m.Step(Event{Kind: Tick}, core.Instant(m.cfg.BackoffMin))
	if !has(as, Dial) || m.attempt != 2 {
		t.Fatalf("joined cancellation did not serve fresh demand: %+v %+v", as, m.Snapshot())
	}
}

func TestLazyBusyCoreProgressAtBudgetBoundary(t *testing.T) {
	m := lazyBusyMachine(t)
	m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrBusy}, 1)
	m.Step(Event{Kind: Released, Attempt: 1}, 2)
	m.Step(Event{Kind: Tick}, m.deadline)
	as := m.Step(Event{Kind: Progress, Attempt: 2, Layer: Cloud}, core.Instant(90*time.Second)+1)
	if has(as, Admit) || !has(as, Release) || !has(as, Reject) {
		t.Fatalf("late progress escaped budget: %+v", as)
	}
	m.Step(Event{Kind: Released, Attempt: 2, ReleasedAllocation: true}, core.Instant(91*time.Second))
	if !has(m.Step(Event{Kind: Client, Client: 2}, core.Instant(91*time.Second)), Dial) {
		t.Fatal("request after late progress blocked")
	}
}

func TestLazyBusyAbandonedReleaseBlocksDemand(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting", true: "canceled"}[cancel], func(t *testing.T) {
			m := lazyBusyMachine(t)
			if cancel {
				m.Step(Event{Kind: ClientGone, Client: 1}, 1)
				m.Step(Event{Kind: Client, Client: 2}, 2)
			}
			m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: errors.Join(ErrBusy, ErrReleaseAbandoned)}, 3)
			as := m.Step(Event{Kind: Released, Attempt: 1}, 4)
			if has(as, Dial) || m.Snapshot().Queued != 0 {
				t.Fatalf("abandoned refusal kept demand: %+v %+v", as, m.Snapshot())
			}
			// Later clean publication and demand cannot erase the owed release.
			m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 5)
			as = m.Step(Event{Kind: Client, Client: 3}, 6)
			if !has(as, Reject) || has(as, Dial) {
				t.Fatalf("replacement allowed: %+v", as)
			}
			as = m.Step(Event{Kind: Shutdown}, 7)
			for _, a := range as {
				if a.Kind == Stopped && errors.Is(a.Err, ErrUncertain) {
					return
				}
			}
			t.Fatalf("shutdown lost owed release: %+v", as)
		})
	}
}

func TestLazyBusyCleanupAbandonmentSurvivesCancellation(t *testing.T) {
	for _, busyFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "late-busy", true: "busy-first"}[busyFirst], func(t *testing.T) {
			m := lazyBusyMachine(t)
			if busyFirst {
				m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
			}
			m.Step(Event{Kind: ClientGone, Client: 1}, 1)
			m.Step(Event{Kind: Client, Client: 2}, 2)
			if !busyFirst {
				m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 3)
			}
			as := m.Step(Event{Kind: Released, Attempt: 1, Err: ErrReleaseAbandoned}, 4)
			if has(as, Dial) || m.Snapshot().State != Busy || m.Snapshot().Queued != 0 {
				t.Fatalf("abandoned cleanup allowed replacement: %+v %+v", as, m.Snapshot())
			}
		})
	}
}
