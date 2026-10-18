package session

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestBusyWinsOverTimeout(t *testing.T) {
	m := machine(t, DefaultConfig())
	m.Step(Event{Kind: Start}, 0)
	now := m.Snapshot().Deadline
	m.Step(Event{Kind: Tick}, now)
	m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrBusy}, now+1)
	m.Step(Event{Kind: Released, Attempt: 1}, now+2)
	as := m.Step(Event{Kind: Tick}, now+core.Instant(time.Hour))
	if has(as, Dial) || m.Snapshot().State != Busy {
		t.Fatalf("late busy retried automatically: snapshot=%+v actions=%+v", m.Snapshot(), as)
	}
}

func TestBusyDuringCleanupPreservesControlPolicy(t *testing.T) {
	for _, first := range []Kind{Tick, Failed, Shutdown} {
		for _, late := range []bool{false, true} {
			m := machine(t, DefaultConfig())
			m.Step(Event{Kind: Start}, 0)
			now := m.Snapshot().Deadline
			if !late {
				m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrBusy}, now-1)
			}
			m.Step(Event{Kind: first, Attempt: 1}, now)
			if as := m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrBusy}, now+1); has(as, Release) {
				t.Fatal("busy issued a second release")
			}
			m.Step(Event{Kind: Released, Attempt: 1}, now+2)
			m.Step(Event{Kind: Tick}, now+core.Instant(time.Hour))
			want := Busy
			if first == Shutdown {
				want = Closed
			}
			if m.Snapshot().State != want || m.Snapshot().Attempt != 1 {
				t.Fatalf("first=%v late=%v: %+v", first, late, m.Snapshot())
			}
		}
	}
}

func TestBusyAfterCleanupStillBlocksCurrentGeneration(t *testing.T) {
	m := machine(t, DefaultConfig())
	m.Step(Event{Kind: Start}, 0)
	m.Step(Event{Kind: Failed, Attempt: 1}, 1)
	m.Step(Event{Kind: Released, Attempt: 1}, 2)
	m.Step(Event{Kind: Client, Client: 2}, 3)
	as := m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrBusy}, 4)
	if !has(as, Reject) || has(as, Release) {
		t.Fatalf("late busy actions=%+v", as)
	}
	if has(m.Step(Event{Kind: Tick}, core.Instant(time.Hour)), Dial) || m.Snapshot().State != Busy {
		t.Fatal("busy received after cleanup was discarded")
	}
}
