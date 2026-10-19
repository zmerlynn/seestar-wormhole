package p2ptunnel

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func (d *driver) advance(at core.Instant) []Action {
	d.t.Helper()
	var actions []Action
	if err := d.scheduler.Run(at, 100, func(e Event, now core.Instant) error {
		d.now = now
		actions = append(actions, d.step(e)...)
		return nil
	}); err != nil {
		d.t.Fatal(err)
	}
	d.now = at
	return actions
}
func requireDeadline(t *testing.T, d *driver, want core.Instant) {
	t.Helper()
	at, ok := d.timer.Deadline()
	if !ok || at != want {
		t.Fatalf("deadline=%d active=%v; want %d", at, ok, want)
	}
	if _, count := d.scheduler.Usage(); count != 1 {
		t.Fatalf("timer count=%d; want 1", count)
	}
}

func TestSimulatorSilentPeersExpireAtExactDeadlines(t *testing.T) {
	for _, role := range []Role{Agent, Server} {
		cfg := DefaultConfig()
		cfg.Role = role
		if role == Server {
			cfg.LoginTimeout = 90 * time.Second
		}
		d := newDriver(t, cfg)
		d.advance(core.Instant(time.Second))
		d.step(Event{Kind: Start})
		at := core.Instant(time.Second + cfg.LoginTimeout)
		requireDeadline(t, d, at)
		d.advance(at - 1)
		if d.m.Status() != LoggingIn {
			t.Fatal("login expired early")
		}
		mustAction(t, d.advance(at), SessionClosed)
		if d.m.Status() != Stopped {
			t.Fatal("silent login did not expire")
		}
		if _, ok := d.timer.Deadline(); ok {
			t.Fatal("stopped login retained timer")
		}
	}
	d := activeDriver(t, DefaultConfig(), true)
	d.advance(core.Instant(2 * time.Second))
	d.step(Event{Kind: Open, Port: 4700, Request: 7})
	at := core.Instant(7 * time.Second)
	requireDeadline(t, d, at)
	d.advance(at - 1)
	if d.m.pending == nil {
		t.Fatal("verification expired early")
	}
	if a := mustAction(t, d.advance(at), OpenFailed); a.Request != 7 {
		t.Fatalf("expired request=%d", a.Request)
	}
	if d.m.pending != nil {
		t.Fatal("verification retained")
	}
}

func TestSimulatorTimerReplacementStaleTicksAndQuarantine(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), true)
	if _, ok := d.timer.Deadline(); ok {
		t.Fatal("successful login failed to cancel timer")
	}
	// Literal successful verification; release starts quarantine at epoch + 29 s.
	d.advance(core.Instant(time.Second))
	d.step(Event{Kind: Open, Port: 4700, Request: 1})
	requireDeadline(t, d, core.Instant(6*time.Second))
	ch := mustAction(t, d.step(Event{Kind: Stream, Data: unhex(t, "0d00060001005c120100")}), Opened)
	d.step(local(Event{Kind: LocalClose}, ch))
	requireDeadline(t, d, core.Instant(30*time.Second))
	d.advance(core.Instant(2 * time.Second))
	d.step(Event{Kind: Open, Port: 4700, Request: 2})
	requireDeadline(t, d, core.Instant(7*time.Second))
	// A previously queued runtime callback may race with replacement. Deliver it
	// alongside input; it must neither expire the new request nor drop its timer.
	if err := d.scheduler.Schedule(core.Instant(6*time.Second), Event{Kind: Tick}, 0); err != nil {
		t.Fatal(err)
	}
	d.advance(core.Instant(6 * time.Second))
	if d.m.pending == nil || d.m.pending.request != 2 {
		t.Fatal("stale timer expired replacement")
	}
	requireDeadline(t, d, core.Instant(7*time.Second))
	mustAction(t, d.advance(core.Instant(7*time.Second)), OpenFailed)
	requireDeadline(t, d, core.Instant(30*time.Second))
	d.advance(core.Instant(30*time.Second) - 1)
	if d.m.Channel(ch.Channel).FreeAt <= d.now {
		t.Fatal("quarantine expired early")
	}
	d.advance(core.Instant(30 * time.Second))
	if _, ok := d.timer.Deadline(); ok {
		t.Fatal("expired quarantine retained timer")
	}
	// A canceled login callback at 30 s is harmless after successful login.
	if d.m.Status() != Active {
		t.Fatal("stale login time stopped active session")
	}
	d.step(Event{Kind: Tick})
	d.step(Event{Kind: Stop})
	d.step(Event{Kind: Tick})
	if _, count := d.scheduler.Usage(); count != 0 {
		t.Fatal("timer remained after stop")
	}
	if d.scheduler.PeakCount > 2 {
		t.Fatal("replaced timers accumulated")
	}
}
