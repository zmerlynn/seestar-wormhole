package session

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

type fixedRNG uint64

func (r fixedRNG) Uint64() uint64 { return uint64(r) }
func machine(t *testing.T, c Config) *Machine {
	t.Helper()
	m, e := New(c, fixedRNG(0))
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func has(as []Action, k Effect) bool {
	for _, a := range as {
		if a.Kind == k {
			return true
		}
	}
	return false
}
func ready(m *Machine, now core.Instant) {
	for l := Cloud; l < LayerCount; l++ {
		m.Step(Event{Kind: Progress, Attempt: m.attempt, Layer: l}, now)
	}
}
func TestLifecycle(t *testing.T) {
	m := machine(t, DefaultConfig())
	if !has(m.Step(Event{Kind: Start}, 0), Dial) {
		t.Fatal("eager start did not dial")
	}
	m.Step(Event{Kind: Client, Client: 1}, 0)
	ready(m, 1)
	if s := m.Snapshot(); s.State != Ready || s.Clients != 1 || s.Queued != 0 {
		t.Fatalf("ready snapshot: %+v", s)
	}
	a := m.Step(Event{Kind: Shutdown}, 2)
	if !has(a, Release) || !has(a, Disconnect) {
		t.Fatalf("shutdown: %+v", a)
	}
	if !has(m.Step(Event{Kind: Released, Attempt: 1}, 3), Stopped) {
		t.Fatal("shutdown never finished")
	}
}
func TestLazyQueueIdleAndRedial(t *testing.T) {
	c := DefaultConfig()
	c.Idle = time.Second
	c.MaxQueued = 1
	m := machine(t, c)
	if has(m.Step(Event{Kind: Start}, 0), Dial) {
		t.Fatal("lazy startup dialed")
	}
	if !has(m.Step(Event{Kind: Client, Client: 1}, 0), Dial) {
		t.Fatal("first client did not dial")
	}
	if a := m.Step(Event{Kind: Client, Client: 2}, 0); !has(a, Reject) || a[0].Err != ErrBudget {
		t.Fatalf("queue overflow: %+v", a)
	}
	ready(m, 0)
	m.Step(Event{Kind: ClientGone, Attempt: 1, Client: 1}, 0)
	if !has(m.Step(Event{Kind: Tick}, core.Instant(time.Second)), Release) {
		t.Fatal("missing first idle release")
	}
	m.Step(Event{Kind: Released, Attempt: 1}, core.Instant(time.Second))
	if m.state != Dormant {
		t.Fatal("idle cleanup did not settle dormant")
	}
	m.Step(Event{Kind: Client, Client: 3}, core.Instant(time.Hour))
	ready(m, core.Instant(time.Hour))
	m.Step(Event{Kind: ClientGone, Attempt: 2, Client: 3}, core.Instant(time.Hour))
	if a := m.Step(Event{Kind: Tick}, core.Instant(time.Hour+time.Second-1)); has(a, Release) {
		t.Fatal("early idle release")
	}
	if a := m.Step(Event{Kind: Tick}, core.Instant(time.Hour+time.Second)); !has(a, Release) {
		t.Fatal("missing idle release")
	}
	m.Step(Event{Kind: Released, Attempt: 2}, core.Instant(time.Hour+time.Second))
	if m.state != Dormant {
		t.Fatalf("idle state = %v", m.state)
	}
}
func TestBusyStaleAndBackoff(t *testing.T) {
	m := machine(t, DefaultConfig())
	m.Step(Event{Kind: Start}, 0)
	m.Step(Event{Kind: Client, Client: 1}, 0)
	a := m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrBusy}, 1)
	if a[0].Err != ErrBusy {
		t.Fatalf("busy client error: %+v", a)
	}
	m.Step(Event{Kind: Released, Attempt: 1}, 2)
	m.Step(Event{Kind: Tick}, core.Instant(time.Hour))
	if m.state != Busy || m.attempt != 1 {
		t.Fatal("busy retried")
	}
	m = machine(t, DefaultConfig())
	m.Step(Event{Kind: Start}, core.Instant(time.Hour))
	m.Step(Event{Kind: Failed, Attempt: 1}, core.Instant(time.Hour))
	m.Step(Event{Kind: Released, Attempt: 1}, core.Instant(time.Hour))
	now := m.deadline
	m.Step(Event{Kind: Tick}, now)
	before := m.Snapshot()
	for _, k := range []Kind{Progress, Failed, Released, Usage, ClientGone} {
		m.Step(Event{Kind: k, Attempt: 1, Bytes: -1}, now)
	}
	before.Discards += 5
	if m.Snapshot() != before {
		t.Fatal("stale event changed state")
	}
	m.Step(Event{Kind: Failed, Attempt: 2}, now)
	m.Step(Event{Kind: Released, Attempt: 2}, now)
	if m.state != Backoff || m.deadline != now.Add(2*m.cfg.BackoffMin) {
		t.Fatalf("backoff snapshot: %+v", m.Snapshot())
	}
	m.Step(Event{Kind: Tick}, m.deadline-1)
	if m.attempt != 2 {
		t.Fatal("early retry")
	}
	m.Step(Event{Kind: Tick}, m.deadline)
	if m.attempt != 3 {
		t.Fatal("missing retry")
	}
}
