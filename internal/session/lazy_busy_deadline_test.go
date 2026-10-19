package session

import (
	"errors"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestBusyBudgetIncludesCleanup(t *testing.T) {
	c := DefaultConfig()
	c.Idle = time.Minute
	c.LazyBusyWindow = LazyBusyWindow
	c.DialTimeout = 120 * time.Second
	m := machine(t, c)
	m.Step(Event{Kind: Start}, 0)
	m.Step(Event{Kind: Client, Client: 1}, 0)
	// Allocator contract permits a partial owned lease together with ErrBusy.
	m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, core.Instant(89*time.Second))
	as := m.Step(Event{Kind: Tick}, core.Instant(90*time.Second))
	if !has(as, Reject) || m.Snapshot().Queued != 0 {
		t.Fatalf("90s budget elapsed during cleanup: actions=%+v state=%+v; want queued clients rejected at 90s", as, m.Snapshot())
	}
}

func TestBusyQueuedClientsShareDeadline(t *testing.T) {
	m := lazyBusyMachine(t)
	m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 1)
	m.Step(Event{Kind: Released, Attempt: 1}, 2)
	m.Step(Event{Kind: Client, Client: 2}, core.Instant(89*time.Second))
	as := m.Step(Event{Kind: Tick}, core.Instant(90*time.Second))
	n := 0
	for _, a := range as {
		if a.Kind == Reject && errors.Is(a.Err, ErrBusy) {
			n++
		}
	}
	if n != 2 || has(as, Dial) {
		t.Fatalf("actions=%+v; want both clients rejected without renewal", as)
	}
}

func TestOlderGenerationBusyCannotPoisonNewDemand(t *testing.T) {
	m := lazyBusyMachine(t)
	m.Step(Event{Kind: ClientGone, Client: 1}, 1)
	m.Step(Event{Kind: Client, Client: 2}, 2)
	m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 3)
	m.Step(Event{Kind: Released, Attempt: 1}, 4)
	before := m.Snapshot()
	as := m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 5)
	if len(as) != 0 || m.Snapshot() != before {
		t.Fatalf("stale outcome mutated new demand: actions=%+v before=%+v after=%+v", as, before, m.Snapshot())
	}
}

func TestNonCloudBusyStopsBeforeFirstRetry(t *testing.T) {
	m := lazyBusyMachine(t)
	as := m.Step(Event{Kind: Failed, Attempt: 1, Layer: DTLS, Err: ErrBusy}, 1)
	m.Step(Event{Kind: Released, Attempt: 1}, 2)
	if !has(as, Reject) {
		t.Errorf("non-cloud refusal retained demand: actions=%+v", as)
	}
	as = m.Step(Event{Kind: Tick}, m.Snapshot().Deadline)
	if has(as, Dial) {
		t.Fatalf("non-cloud refusal retried inside 90s: actions=%+v", as)
	}
}
