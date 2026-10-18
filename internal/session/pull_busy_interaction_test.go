package session

import (
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestReliabilityPullSpentBusyWindowCannotRevive(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Idle = time.Hour
	cfg.LazyBusyWindow = 90 * time.Second
	cfg.SingleBusyWindow = true
	m := machine(t, cfg)
	m.Step(Event{Kind: Start}, 0)
	m.Step(Event{Kind: Client, Client: 1}, 0)
	ready(m, 0)
	now := core.Instant(10 * time.Minute)
	m.Step(Event{Kind: Failed, Attempt: 1, Layer: Rendezvous, Err: ErrTransport}, now)
	m.Step(Event{Kind: Released, Attempt: 1}, now+1)
	m.Step(Event{Kind: Client, Client: 2}, now+2)
	now = m.Snapshot().Deadline
	if !has(m.Step(Event{Kind: Tick}, now), Dial) {
		t.Fatal("post-ready transport recovery did not dial")
	}
	m.Step(Event{Kind: Failed, Attempt: 2, Layer: Cloud, Err: ErrBusy}, now)
	m.Step(Event{Kind: Released, Attempt: 2}, now+1)
	if m.Snapshot().State != Busy {
		t.Fatalf("spent recovery refusal state=%+v", m.Snapshot())
	}
	as := m.Step(Event{Kind: Client, Client: 3}, now+2)
	if has(as, Dial) || !has(as, Reject) || m.Snapshot().Attempt != 2 {
		t.Fatalf("fresh backend retry reopened spent pull busy budget: actions=%+v snapshot=%+v; want Reject and attempt=2", as, m.Snapshot())
	}
}
