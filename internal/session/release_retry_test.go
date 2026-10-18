package session

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestReleaseAbandonmentBacksOffAfterJoin(t *testing.T) {
	m := machine(t, DefaultConfig())
	m.Step(Event{Kind: Start}, 0)
	ready(m, 0)
	m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrTransport}, core.Instant(31*time.Second))
	deadline := m.Snapshot().Deadline
	// A cloud timeout and the owner timer may both become ready at this instant.
	as := m.Step(Event{Kind: Tick}, deadline)
	if has(as, Stopped) || has(as, Dial) || m.Snapshot().State != Releasing {
		t.Fatalf("deadline before worker join: %+v %+v", m.Snapshot(), as)
	}
	as = m.Step(Event{Kind: Released, Attempt: 1, Err: ErrReleaseAbandoned}, deadline)
	if has(as, Dial) || m.Snapshot().State != Backoff {
		t.Fatalf("abandoned release: %+v %+v", m.Snapshot(), as)
	}
	if got := m.Snapshot().Deadline - deadline; got < core.Instant(500*time.Millisecond) || got > core.Instant(time.Second) {
		t.Fatalf("retry delay=%v, want 500ms..1s", got)
	}
	if has(m.Step(Event{Kind: Tick}, m.Snapshot().Deadline-1), Dial) {
		t.Fatal("redial before backoff")
	}
	if !has(m.Step(Event{Kind: Tick}, m.Snapshot().Deadline), Dial) {
		t.Fatal("no redial after bounded abandonment")
	}
}
