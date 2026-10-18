package p2ptunnel

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestTelemetryNativeSameStepAndDecisionQ(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 65536
	d := activeDriver(t, cfg, false)
	d.m.EnableTelemetry()
	a := d.open(4700)
	d.now = core.Instant(time.Second)
	d.m.Step(Event{Kind: ReceiveQueueSize, Count: 65537}, d.now)
	// DATA followed by CLOSE in one independent literal stream.
	d.m.Step(Event{Kind: Stream, Data: []byte{2, a.Channel, 3, 0, 1, 2, 3, 4, a.Channel, 1, 0, 1}}, d.now)
	s := d.m.Telemetry(a.Channel, core.Instant(3*time.Second))
	if s.AdmittedPackets != 1 || s.AdmittedBytes != 3 || s.Pause != 1 || s.Paused != 0 || s.IsPaused || s.DeliveryBytes != 0 || s.DeliveryAge != 0 || s.DecisionQ != 65540 || s.HighWater != 65536 || s.LowWater != 52428 {
		t.Fatalf("native=%+v", s)
	}
}

func TestTelemetryNativeMultipleDecisionsAndFailure(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 65536
	d := activeDriver(t, cfg, false)
	d.m.EnableTelemetry()
	a := d.open(4700)
	d.now = core.Instant(time.Second)
	write := mustAction(t, d.frame(Frame{Command: DataCommand, Channel: a.Channel, Payload: []byte("abc")}), WriteLocal)
	d.m.Step(Event{Kind: ReceiveQueueSize, Count: 65537}, d.now)
	d.now = core.Instant(2 * time.Second)
	d.m.Step(Event{Kind: ReceiveQueueSize, Count: 0}, d.now)
	d.now = core.Instant(3 * time.Second)
	d.m.Step(Event{Kind: LocalWriteFailed, Channel: a.Channel, Generation: write.Generation, Token: write.Token}, core.Instant(4*time.Second))
	s := d.m.Telemetry(a.Channel, core.Instant(5*time.Second))
	if s.Pause != 1 || s.Resume != 1 || s.Paused != time.Second || s.DecisionQ != 3 || s.DecisionQMax != 65540 || s.DeliveryBytes != 0 || s.DeliveryAge != 0 {
		t.Fatalf("native=%+v", s)
	}
}

func TestTelemetryNativeOffAndPartialControl(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 2
	d := activeDriver(t, cfg, false)
	if d.m.telemetry != nil || d.m.Telemetry(0, 0) != (ChannelTelemetry{}) {
		t.Fatal("disabled core telemetry has state")
	}
	d.m.EnableTelemetry()
	a := d.open(4700)
	d.auto = false
	send := mustAction(t, d.step(Event{Kind: Stream, Data: []byte{2, a.Channel, 3, 0, 1, 2, 3}}), Send)
	tail := mustAction(t, d.step(Event{Kind: Sent, Token: send.Token, Count: 1}), Send)
	d.step(Event{Kind: Sent, Token: send.Token, Count: 3})
	if s := d.m.Telemetry(a.Channel, 0); s.PauseSent != 0 {
		t.Fatalf("partial/stale counted=%+v", s)
	}
	d.step(Event{Kind: Sent, Token: tail.Token, Count: 3})
	if s := d.m.Telemetry(a.Channel, 0); s.PauseSent != 1 {
		t.Fatalf("full counted=%+v", s)
	}
}

func TestTelemetryNativeStageConservation(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	d.m.EnableTelemetry()
	a := d.open(4700)
	d.now = core.Instant(time.Second)
	w := mustAction(t, d.frame(Frame{Command: DataCommand, Channel: a.Channel, Payload: []byte("abc")}), WriteLocal)
	aa := d.step(local(Event{Kind: LocalWritten, Count: 1}, w))
	w = mustAction(t, aa, WriteLocal)
	d.step(local(Event{Kind: LocalWriteFailed}, w))
	s := d.m.Telemetry(a.Channel, d.now)
	if s.AdmittedBytes != 3 || s.DeliveredBytes != 1 || s.DiscardedBytes != 2 || s.AdmittedBytes != s.DeliveredBytes+s.DiscardedBytes+uint64(d.m.Channel(a.Channel).Queued) {
		t.Fatalf("stage conservation=%+v", s)
	}
}

func FuzzTunnelTelemetry(f *testing.F) {
	f.Add([]byte{0, 1, 0, 2, 3, 4, 5, 6, 7})
	f.Add([]byte{0, 0, 2, 0, 3, 0, 4, 2, 5})
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1024 {
			b = b[:1024]
		}
		d := activeDriver(t, DefaultConfig(), false)
		d.m.EnableTelemetry()
		a := d.open(4700)
		var write Action
		for _, v := range b {
			d.now += core.Instant(time.Millisecond) * core.Instant(v)
			e := Event{Kind: Tick}
			switch v % 8 {
			case 0:
				e = Event{Kind: Stream, Data: []byte{2, a.Channel, 1, 0, v}}
			case 1:
				e = Event{Kind: ReceiveQueueSize, Count: 65537}
			case 2:
				e = Event{Kind: ReceiveQueueSize, Count: 0}
			case 3:
				e = local(Event{Kind: LocalWritten, Count: len(write.Data)}, write)
			case 4:
				e = local(Event{Kind: LocalWriteFailed}, write)
			case 5:
				e = local(Event{Kind: LocalWritten, Count: 1, Token: write.Token + 1}, write)
				e.Token++
			case 6:
				e = Event{Kind: Stream, Data: b}
			case 7:
				e = Event{Kind: Stop}
			}
			for _, x := range d.step(e) {
				if x.Kind == WriteLocal {
					write = x
				}
			}
			s := d.m.Telemetry(a.Channel, d.now)
			c := d.m.Channel(a.Channel)
			if s.AdmittedBytes != s.DeliveredBytes+s.DiscardedBytes+uint64(c.Queued) || s.DeliveryBytes != d.m.QueueStats(a.Channel, d.now).DeliveryBytes || !c.Active && (s.IsPaused || s.DeliveryBytes != 0) {
				t.Fatal("native conservation", s, c)
			}
		}
	})
}
