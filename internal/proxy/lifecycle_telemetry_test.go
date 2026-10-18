package proxy

import (
	"bytes"
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"io"
	"log/slog"
	"testing"
	"time"
)

func activeTunnel(t *testing.T, high int) (*stack, p2ptunnel.Action) {
	t.Helper()
	var logs bytes.Buffer
	x := telemetryStack(t, time.Second, &logs)
	cfg := p2ptunnel.DefaultConfig()
	cfg.HighWater = high
	x.tunnel, _ = p2ptunnel.New(cfg)
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Start}, 0)
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{8, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}}, 0)
	var opened p2ptunnel.Action
	for _, a := range telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Open, Port: 4700, Request: 1}, 0) {
		if a.Kind == p2ptunnel.Opened {
			opened = a
		}
	}
	return x, opened
}

func TestPauseAndCloseSameStep(t *testing.T) {
	x, opened := activeTunnel(t, 2)
	// Independent literal DATA then CLOSE in a single reconstructed-stream event.
	data := []byte{2, opened.Channel, 3, 0, 1, 2, 3, 4, opened.Channel, 1, 0, 1}
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: data}, core.Instant(time.Second))
	if x.tunnel.Channel(opened.Channel).Active {
		t.Fatal("channel still active")
	}
	s := x.telemetry.channels[opened.Channel]
	if s.isPaused {
		t.Fatalf("closed channel remains paused: pause=%d isPaused=%v pauseAt=%d; every future stats record adds time on a closed channel", s.pause, s.isPaused, s.pauseAt)
	}
}

func TestDeliveryAfterWriteFailure(t *testing.T) {
	x, opened := activeTunnel(t, 65536)
	var write p2ptunnel.Action
	for _, a := range telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{2, opened.Channel, 3, 0, 1, 2, 3}}, core.Instant(time.Second)) {
		if a.Kind == p2ptunnel.WriteLocal {
			write = a
		}
	}
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.LocalWriteFailed, Channel: write.Channel, Generation: write.Generation, Token: write.Token}, core.Instant(2*time.Second))
	if q := x.tunnel.QueueStats(opened.Channel, core.Instant(2*time.Second)); q.DeliveryBytes != 0 || q.RXBytes != 0 {
		t.Fatalf("core queue did not drain: %+v", q)
	}
	if got := x.telemetry.channels[opened.Channel].delivery; got != 0 {
		t.Fatalf("telemetry delivery_bytes=%d want 0 after valid local write failure; delivery_age grows forever", got)
	}
}

func TestPressureLogBound(t *testing.T) {
	var logs bytes.Buffer
	x := telemetryStack(t, time.Second, &logs)
	x.reliable, _ = rdt.New(rdt.DefaultConfig())
	// rdtAction is the production dispatch for every rejected DATA packet.
	for range 1000 {
		x.rdtAction(rdt.Action{Kind: rdt.ReceivePressure})
	}
	got := bytes.Count(logs.Bytes(), []byte{'\n'})
	if got > debugRecordsPerInterval {
		t.Fatalf("pressure debug records=%d want <=%d per interval", got, debugRecordsPerInterval)
	}
}

func BenchmarkActiveTunnelTelemetry(b *testing.B) {
	for _, on := range []bool{false, true} {
		name := "off"
		if on {
			name = "on"
		}
		b.Run(name, func(b *testing.B) {
			m, _ := p2ptunnel.New(p2ptunnel.DefaultConfig())
			m.Step(p2ptunnel.Event{Kind: p2ptunnel.Start}, 0)
			m.Step(p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{8, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}}, 0)
			x := &stack{tunnel: m, f: &factory{s: &Session{statsInterval: time.Second, logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}}}
			if on {
				x.initTelemetry(0)
			}
			e := p2ptunnel.Event{Kind: p2ptunnel.Tick}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				x.tunnelStep(e, 0)
			}
		})
	}
}

func TestNoGoroutineGrowth(t *testing.T) {
	// Reuse the composed synctest fixture repeatedly; its bubble requires all
	// workers to exit, so a lingering worker fails/deadlocks each iteration.
	for range 5 {
		TestTelemetryComposedPeriodicAndWaits(t)
	}
	_ = context.Background()
}
