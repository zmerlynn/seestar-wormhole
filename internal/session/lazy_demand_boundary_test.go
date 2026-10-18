package session

import (
	"errors"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// Reject removes the old demand from the core; clients need not cancel it again.
// A later caller must get its own budget from its own first Dial.

func TestFreshDemandAfterDialTimeout(t *testing.T) {
	m := lazyBusyMachine(t)
	as := m.Step(Event{Kind: Tick}, core.Instant(30*time.Second))
	if !has(as, Reject) || !has(as, Release) {
		t.Fatalf("first dial timeout=%+v", as)
	}
	m.Step(Event{Kind: Released, Attempt: 1}, core.Instant(30*time.Second))
	m.Step(Event{Kind: Tick}, core.Instant(31*time.Second))
	m.Step(Event{Kind: Client, Client: 2}, core.Instant(40*time.Second))
	if m.Snapshot().State != Dialing {
		t.Fatalf("fresh demand did not dial: %+v", m.Snapshot())
	}
	m.Step(Event{Kind: Failed, Attempt: 2, Layer: Cloud, Err: ErrBusy}, core.Instant(40*time.Second))
	m.Step(Event{Kind: Released, Attempt: 2}, core.Instant(40*time.Second))
	for now := m.Snapshot().Deadline; now < core.Instant(130*time.Second); now = m.Snapshot().Deadline {
		as = m.Step(Event{Kind: Tick}, now)
		if has(as, Reject) {
			t.Fatalf("fresh caller rejected at %v (only %v after its first dial); want 130s: %+v", time.Duration(now), time.Duration(now-core.Instant(40*time.Second)), as)
		}
		if !has(as, Dial) {
			t.Fatalf("no retry at %v: %+v %+v", now, as, m.Snapshot())
		}
		id := m.Snapshot().Attempt
		m.Step(Event{Kind: Failed, Attempt: id, Layer: Cloud, Err: ErrBusy}, now)
		m.Step(Event{Kind: Released, Attempt: id}, now)
	}
	as = m.Step(Event{Kind: Tick}, core.Instant(130*time.Second))
	if !has(as, Reject) || has(as, Dial) {
		t.Fatalf("fresh caller deadline=%+v", as)
	}
}

func TestCoincidentCleanupAndDemandDeadline(t *testing.T) {
	for _, failure := range []error{nil, ErrStop, ErrUncertain, ErrTransport} {
		for _, kind := range []Kind{Tick, Released, Client, Shutdown} {
			m := lazyBusyMachine(t)
			// StopTimeout=5s: cleanup and client budget both end at 90s.
			m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, core.Instant(85*time.Second))
			if failure != nil {
				m.Step(Event{Kind: Failed, Attempt: 1, Layer: Rendezvous, Err: failure}, core.Instant(86*time.Second))
			}
			as := m.Step(Event{Kind: kind, Attempt: 1, Client: 2}, core.Instant(90*time.Second))
			rejectedOld := false
			for _, a := range as {
				if a.Kind == Reject && a.Client == 1 && errors.Is(a.Err, ErrBusy) {
					rejectedOld = true
				}
				if a.Kind == ArmTimer && a.Deadline <= core.Instant(90*time.Second) {
					t.Fatalf("failure=%v event=%v rearmed expired timer: %+v", failure, kind, as)
				}
			}
			if !rejectedOld {
				t.Fatalf("failure=%v event=%v missed old demand expiry: %+v", failure, kind, as)
			}
			if kind != Released && has(as, Dial) {
				t.Fatalf("replacement before join: %+v", as)
			}
			if kind == Tick {
				repeated := m.Step(Event{Kind: Tick}, core.Instant(90*time.Second))
				if len(repeated) != 0 {
					t.Fatalf("repeated same-instant tick changed state: %+v", repeated)
				}
			}
		}
	}
}

func TestFreshClientAtDemandExpiry(t *testing.T) {
	for _, phase := range []string{"backoff", "dial", "release"} {
		m := lazyBusyMachine(t)
		m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
		m.Step(Event{Kind: Released, Attempt: 1}, 0)
		if phase != "backoff" {
			m.Step(Event{Kind: Tick}, m.Snapshot().Deadline)
		}
		if phase == "release" {
			m.Step(Event{Kind: Failed, Attempt: 2, Layer: Cloud, Err: ErrBusy}, core.Instant(89*time.Second))
		}
		as := m.Step(Event{Kind: Client, Client: 2}, core.Instant(90*time.Second))
		for _, a := range as {
			if a.Kind == Reject && a.Client == 2 {
				t.Fatalf("phase=%s fresh caller inherited expired budget: %+v", phase, as)
			}
		}
		if phase != "backoff" {
			if has(as, Dial) {
				t.Fatalf("phase=%s replacement before join: %+v", phase, as)
			}
			as = m.Step(Event{Kind: Released, Attempt: 2}, core.Instant(93*time.Second))
			// Late refusal must not restart the old-generation budget.
			if !has(as, Dial) {
				t.Fatalf("phase=%s successful join did not dial: %+v", phase, as)
			}
		}
		dialAt := core.Instant(90 * time.Second)
		if phase != "backoff" {
			dialAt = core.Instant(93 * time.Second)
		}
		id := m.Snapshot().Attempt
		m.Step(Event{Kind: Failed, Attempt: id, Layer: Cloud, Err: ErrBusy}, dialAt)
		m.Step(Event{Kind: Released, Attempt: id}, dialAt)
		if m.lazyUntil != dialAt+core.Instant(90*time.Second) {
			t.Fatalf("phase=%s deadline=%v want %v", phase, m.lazyUntil, dialAt+core.Instant(90*time.Second))
		}
	}
}
