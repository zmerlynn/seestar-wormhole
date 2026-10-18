package p2ptunnel

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

// Unequal holders report their attributed queue share, and a PAUSE in the
// separate flow store counts only after acceptance.
func TestTelemetryFairPauseAttributionAndEmission(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 49
	d := activeDriver(t, cfg, false)
	d.m.EnableTelemetry()
	a, b := d.open(445), d.open(4700)
	d.frame(Frame{DataCommand, a.Channel, make([]byte, 75)})
	d.frame(Frame{DataCommand, b.Channel, make([]byte, 25)})
	d.step(Event{Kind: ReceiveQueueSize, Count: 100})
	s := d.m.Telemetry(b.Channel, d.now)
	if s.DecisionQ != 50 || s.Pause != 1 || s.PauseSent != 1 {
		t.Fatalf("Q/retention/emission=%+v want Q=50 PAUSE retained=1 sent=1", s)
	}
	if !d.hasWire(PauseCommand) {
		t.Fatal("missing PAUSE wire")
	}
}

func TestTelemetryFairPausePartialAcceptance(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 2
	d := activeDriver(t, cfg, false)
	d.m.EnableTelemetry()
	a := d.open(4700)
	d.auto = false
	out := d.step(Event{Kind: Stream, Data: []byte{2, a.Channel, 3, 0, 1, 2, 3}})
	send := mustAction(t, out, Send)
	if !bytes.Equal(send.Data, []byte{5, a.Channel, 0, 0}) {
		t.Fatalf("wire=%x want literal PAUSE", send.Data)
	}
	if s := d.m.Telemetry(a.Channel, d.now); s.Pause != 1 || s.PauseSent != 0 {
		t.Fatalf("selected control=%+v", s)
	}
	if s := d.m.Telemetry(a.Channel, d.now); !s.IsPaused || s.AcceptedPaused || s.PendingControl != PauseCommand || s.HandoffControl != PauseCommand || s.HandoffAcceptedBytes != 0 {
		t.Fatalf("desired/accepted/selected=%+v", s)
	}
	d.step(Event{Kind: Sent, Token: send.Token, Count: 0})
	if s := d.m.Telemetry(a.Channel, d.now); s.PauseSent != 0 {
		t.Fatalf("zero acceptance counted=%+v", s)
	}
	send = mustAction(t, d.step(Event{Kind: TransportWritable}), Send)
	tail := mustAction(t, d.step(Event{Kind: Sent, Token: send.Token, Count: 1}), Send)
	d.step(Event{Kind: Sent, Token: send.Token, Count: 3}) // stale predecessor
	if s := d.m.Telemetry(a.Channel, d.now); s.PauseSent != 0 {
		t.Fatalf("partial/stale acceptance counted=%+v", s)
	}
	if s := d.m.Telemetry(a.Channel, d.now); s.HandoffControl != PauseCommand || s.HandoffAcceptedBytes != 1 || s.AcceptedPaused {
		t.Fatalf("partial state=%+v", s)
	}
	d.step(Event{Kind: Sent, Token: tail.Token, Count: 3})
	d.step(Event{Kind: Sent, Token: tail.Token, Count: 3}) // duplicate completion
	if s := d.m.Telemetry(a.Channel, d.now); s.PauseSent != 1 || !s.AcceptedPaused || s.PendingControl != 0 || s.HandoffControl != 0 {
		t.Fatalf("completed control=%+v want exactly one PAUSE", s)
	}
}

func TestTelemetryFairPauseCanceledAndImmutableResume(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 100
	d := activeDriver(t, cfg, false)
	d.m.EnableTelemetry()
	a := d.open(445)
	d.frame(Frame{DataCommand, a.Channel, []byte{1}})
	d.auto = false
	pause := mustAction(t, d.step(Event{Kind: ReceiveQueueSize, Count: 101}), Send)
	d.step(Event{Kind: ReceiveQueueSize, Count: 0})
	d.step(Event{Kind: Sent, Token: pause.Token, Count: 0})
	if s := d.m.Telemetry(a.Channel, d.now); s.Pause != 1 || s.Resume != 1 || s.PauseSent != 0 || s.ResumeSent != 0 || s.IsPaused || s.AcceptedPaused || s.PendingControl != 0 || s.HandoffControl != 0 {
		t.Fatalf("canceled pair=%+v", s)
	}
	d.step(Event{Kind: TransportWritable})
	pause = mustAction(t, d.step(Event{Kind: ReceiveQueueSize, Count: 101}), Send)
	d.step(Event{Kind: Sent, Token: pause.Token, Count: 4})
	resume := mustAction(t, d.step(Event{Kind: ReceiveQueueSize, Count: 0}), Send)
	tail := mustAction(t, d.step(Event{Kind: Sent, Token: resume.Token, Count: 1}), Send)
	d.step(Event{Kind: ReceiveQueueSize, Count: 101})
	if s := d.m.Telemetry(a.Channel, d.now); !s.IsPaused || !s.AcceptedPaused || s.HandoffControl != ResumeCommand || s.HandoffAcceptedBytes != 1 || s.ResumeSent != 0 {
		t.Fatalf("immutable RESUME and new desired PAUSE=%+v", s)
	}
	pause = mustAction(t, d.step(Event{Kind: Sent, Token: tail.Token, Count: 3}), Send)
	if !bytes.Equal(pause.Data, []byte{5, a.Channel, 0, 0}) {
		t.Fatalf("successor wire=%x", pause.Data)
	}
	if s := d.m.Telemetry(a.Channel, d.now); !s.IsPaused || s.AcceptedPaused || s.ResumeSent != 1 || s.PauseSent != 1 {
		t.Fatalf("completed RESUME and pending PAUSE=%+v", s)
	}
	d.step(Event{Kind: Sent, Token: pause.Token, Count: 4})
	if s := d.m.Telemetry(a.Channel, d.now); s.PauseSent != 2 || s.ResumeSent != 1 || !s.AcceptedPaused {
		t.Fatalf("accepted successor=%+v", s)
	}
}

func TestTelemetryFairPauseCoalescedWakeAndFailure(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 100
	d := activeDriver(t, cfg, false)
	d.m.EnableTelemetry()
	a := d.open(445)
	d.frame(Frame{DataCommand, a.Channel, bytes.Repeat([]byte{'a'}, 101)})
	d.auto = false
	r := mustAction(t, d.step(local(Event{Kind: ReadReady}, a)), ReadLocal)
	data := mustAction(t, d.step(local(Event{Kind: LocalData, Data: []byte{'D'}}, r)), Send)
	for range 1094 {
		d.m.control(WakeCommand, a.Channel)
	}
	if s := d.m.Telemetry(a.Channel, d.now); s.Wake != 1 || s.WakeSent != 0 || !s.WakePending || s.PendingControl != WakeCommand {
		t.Fatalf("coalesced opportunities=%+v", s)
	}
	wake := mustAction(t, d.step(Event{Kind: Sent, Token: data.Token, Count: len(data.Data)}), Send)
	if !bytes.Equal(wake.Data, []byte{7, a.Channel, 0, 0}) {
		t.Fatalf("WAKE wire=%x", wake.Data)
	}
	d.step(Event{Kind: Sent, Token: wake.Token, Count: 0})
	wake = mustAction(t, d.step(Event{Kind: TransportWritable}), Send)
	tail := mustAction(t, d.step(Event{Kind: Sent, Token: wake.Token, Count: 1}), Send)
	if s := d.m.Telemetry(a.Channel, d.now); s.Wake != 1 || s.WakeSent != 0 || s.HandoffAcceptedBytes != 1 {
		t.Fatalf("partial WAKE=%+v", s)
	}
	d.step(Event{Kind: Sent, Token: tail.Token, Count: 3})
	if s := d.m.Telemetry(a.Channel, d.now); s.WakeSent != 1 || s.PendingControl != 0 {
		t.Fatalf("accepted WAKE=%+v", s)
	}
	d.m.control(WakeCommand, a.Channel)
	wake = mustAction(t, d.step(Event{Kind: Tick}), Send)
	d.step(Event{Kind: Sent, Token: wake.Token, Failed: true})
	if s := d.m.Telemetry(a.Channel, d.now); s.WakeSent != 1 || s.IsPaused || s.AcceptedPaused || s.HandoffControl != 0 {
		t.Fatalf("failed WAKE=%+v", s)
	}
}

func TestTelemetryFairPauseReleasedGenerationCompletion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 2
	d := activeDriver(t, cfg, false)
	d.m.EnableTelemetry()
	a := d.open(445)
	d.auto = false
	pause := mustAction(t, d.frame(Frame{DataCommand, a.Channel, []byte{1, 2, 3}}), Send)
	tail := mustAction(t, d.step(Event{Kind: Sent, Token: pause.Token, Count: 1}), Send)
	d.step(local(Event{Kind: LocalClose}, a))
	d.now = core.Instant(29 * time.Second)
	for range 255 {
		d.open(445)
	}
	b := d.open(445)
	if b.Channel != a.Channel || b.Generation == a.Generation {
		t.Fatalf("reuse old=%+v new=%+v", a, b)
	}
	d.step(Event{Kind: Sent, Token: tail.Token, Count: 3})
	if s := d.m.Telemetry(b.Channel, d.now); s.PauseSent != 1 || s.IsPaused || s.AcceptedPaused || s.HandoffControl != 0 || s.PendingControl != 0 || s.Paused != 0 {
		t.Fatalf("old emitted frame must not pause new generation=%+v", s)
	}
}
