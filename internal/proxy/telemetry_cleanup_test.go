package proxy

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"testing"
	"time"
)

func TestTelemetrySameStepDeliveryClose(t *testing.T) {
	x, opened := activeTunnel(t, 2)
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{2, opened.Channel, 3, 0, 1, 2, 3, 4, opened.Channel, 1, 0, 1}}, core.Instant(time.Second))
	if s := x.telemetry.channels[opened.Channel]; s.delivery != 0 || s.isPaused {
		t.Fatalf("closed delivery/pause=%+v", s)
	}
}

func TestTelemetryStaleFailureKeepsDeliveryAge(t *testing.T) {
	x, opened := activeTunnel(t, 65536)
	var write p2ptunnel.Action
	for _, a := range telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{2, opened.Channel, 3, 0, 1, 2, 3}}, core.Instant(time.Second)) {
		if a.Kind == p2ptunnel.WriteLocal {
			write = a
		}
	}
	for _, e := range []p2ptunnel.Event{
		{Kind: p2ptunnel.LocalWriteFailed, Channel: write.Channel, Generation: write.Generation, Token: write.Token + 1},
		{Kind: p2ptunnel.LocalWriteFailed, Channel: write.Channel, Generation: write.Generation + 1, Token: write.Token},
	} {
		telemetryTunnelStep(x, e, core.Instant(2*time.Second))
	}
	if s := x.telemetry.channels[opened.Channel]; s.delivery != 3 || s.deliveryAt != core.Instant(time.Second) {
		t.Fatalf("stale failure changed delivery=%+v", s)
	}
}

func TestTelemetryOdditiesShareBudget(t *testing.T) {
	var logs bytes.Buffer
	x := telemetryStack(t, time.Second, &logs)
	for range 1000 {
		x.rdtAction(rdt.Action{Kind: rdt.ReceiveOddity})
		x.tunnelAction(p2ptunnel.Action{Kind: p2ptunnel.ReceiveOddity})
	}
	if got := bytes.Count(logs.Bytes(), []byte{'\n'}); got != debugRecordsPerInterval || x.telemetry.debugDropped != 2000-debugRecordsPerInterval {
		t.Fatalf("records=%d dropped=%d", got, x.telemetry.debugDropped)
	}
}

func TestTelemetryEpochExhaustionStopsPeriodic(t *testing.T) {
	var logs bytes.Buffer
	x := telemetryStack(t, time.Second, &logs)
	x.stats(core.Instant(1<<63 - 1))
	n := logs.Len()
	for range 3 {
		x.stats(core.Instant(1<<63 - 1))
	}
	if logs.Len() != n {
		t.Fatal("exhausted epoch keeps emitting")
	}
}

func TestTelemetryOffKeepsBoundedLegacyOddities(t *testing.T) {
	var logs bytes.Buffer
	x := telemetryStack(t, 0, &logs)
	for range 1000 {
		x.rdtAction(rdt.Action{Kind: rdt.ReceiveOddity})
	}
	if got := bytes.Count(logs.Bytes(), []byte{'\n'}); got != debugRecordsPerInterval {
		t.Fatalf("off legacy records=%d want %d", got, debugRecordsPerInterval)
	}
	if x.telemetry != nil || x.sendMarks != nil || logs.Len() == 0 {
		t.Fatal("off collection allocated state or lost legacy oddity logs")
	}
}
