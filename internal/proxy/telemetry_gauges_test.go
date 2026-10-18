package proxy

import (
	"bytes"
	"context"
	"fmt"
	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"io"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"
)

func TestTelemetryPeriodicDiagnosisFields(t *testing.T) {
	var logs bytes.Buffer
	x := telemetryStack(t, time.Second, &logs)
	cfg := rdt.DefaultConfig()
	cfg.Telemetry = true
	x.reliable, _ = rdt.New(cfg)
	x.stats(core.Instant(time.Second))
	var got map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		var v map[string]any
		if err := json.Unmarshal(line, &v, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		if v["msg"] == "rdt_stats" {
			got = v
		}
	}
	for _, key := range []string{"cwnd_packets", "tx_packets", "unsent_packets", "unsent_bytes", "in_flight_packets", "in_flight_bytes", "retransmit_sack", "retransmit_ack", "retransmit_rto", "retransmit_pacing", "ack_received", "sack_received", "sack_ranges_received", "out_of_order_arrivals", "reorder_distance_max", "feedback_age_ms"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing operator field %s", key)
		}
	}
}

func TestTelemetryPeriodicFairPauseStorage(t *testing.T) {
	x, a := activeTunnel(t, 49)
	var logs bytes.Buffer
	x.f.s.logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var b p2ptunnel.Action
	for _, v := range telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Open, Port: 445}, 0) {
		if v.Kind == p2ptunnel.Opened {
			b = v
		}
	}
	for _, v := range []struct {
		ch byte
		n  int
	}{{a.Channel, 75}, {b.Channel, 25}} {
		data := append([]byte{2, v.ch, byte(v.n), 0}, bytes.Repeat([]byte{0x9a}, v.n)...)
		telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: data}, 0)
	}
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: 100}, 0)
	x.stats(core.Instant(time.Second))
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		var v map[string]any
		if err := json.Unmarshal(line, &v, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		if v["msg"] != "tunnel_stats" || v["channel"] != float64(b.Channel) {
			continue
		}
		for key, want := range map[string]any{"high_water": float64(49), "low_water": float64(39), "pressure_q": float64(50), "decision_q": float64(50), "attributed_rx_bytes": float64(25), "paused": true, "accepted_paused": false, "pending_control": float64(5), "handoff_control": float64(0), "handoff_accepted_bytes": float64(0), "wake_pending": false, "pause_queued": float64(1), "pause_sent": float64(0)} {
			if v[key] != want {
				t.Errorf("%s=%v want %v", key, v[key], want)
			}
		}
		return
	}
	t.Fatal("missing second holder's periodic stats")
}

func TestTelemetrySecureQueueByteConservation(t *testing.T) {
	var logs bytes.Buffer
	x := telemetryStack(t, time.Second, &logs)
	x.secureTX = make(chan []byte, 16)
	cfg := rdt.DefaultConfig()
	cfg.Telemetry = true
	x.reliable, _ = rdt.New(cfg)
	for _, data := range [][]byte{{1, 2, 3}, {4, 5, 6, 7, 8}} {
		x.rdtAction(rdt.Action{Kind: rdt.Send, Data: data})
	}
	if x.telemetry.secureQueueBytes != 8 || x.telemetry.secureQueueCount != 2 {
		t.Fatalf("queued=%d/%d", x.telemetry.secureQueueBytes, x.telemetry.secureQueueCount)
	}
	x.drive(layerEvent{r: &rdt.Event{Kind: rdt.SendComplete}}, 0)
	if x.telemetry.secureQueueBytes != 5 || x.telemetry.secureQueueCount != 1 {
		t.Fatalf("first completion=%d/%d", x.telemetry.secureQueueBytes, x.telemetry.secureQueueCount)
	}
	x.drive(layerEvent{r: &rdt.Event{Kind: rdt.SendComplete}}, 0)
	if x.telemetry.secureQueueBytes != 0 || x.telemetry.secureQueueCount != 0 {
		t.Fatalf("drained=%d/%d", x.telemetry.secureQueueBytes, x.telemetry.secureQueueCount)
	}
}

func TestTelemetryExactAdmissionOnSameStepClose(t *testing.T) {
	x, opened := activeTunnel(t, 2)
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{2, opened.Channel, 3, 0, 1, 2, 3, 4, opened.Channel, 1, 0, 1}}, core.Instant(time.Second))
	if got := x.telemetry.channels[opened.Channel].admitted; got != 3 {
		t.Fatalf("same-Step admitted=%d want 3", got)
	}
}

func TestTelemetryOwnerWaitDuration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		x := telemetryStack(t, time.Second, &logs)
		x.f.s.work = make(chan func(core.Instant), 1)
		if !x.post(func(core.Instant) {}) {
			t.Fatal("post failed")
		}
		timer := time.NewTimer(25 * time.Millisecond)
		<-timer.C
		(<-x.f.s.work)(core.Instant(25 * time.Millisecond))
		if s := x.telemetry.ownerWait; s.count != 1 || s.total != 25*time.Millisecond || s.max != 25*time.Millisecond {
			t.Fatalf("owner wait=%+v want one 25ms wait", s)
		}
	})
}

func TestTelemetryWriterAndQueueWaitUnderPressure(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s, stacks, _ := workloadSession(t, ctx, func(p *simulatedWire) { p.delay = 5 * time.Millisecond }, Config{StatsInterval: time.Second, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		if err := exchange(c.(*stream), 12, 6000); err != nil {
			t.Fatal(err)
		}
		x := <-stacks
		done := make(chan struct{})
		x.post(func(core.Instant) {
			if x.telemetry.sendWait.max < 5*time.Millisecond || x.telemetry.sendQueueWait.max < 5*time.Millisecond {
				t.Errorf("send wait=%+v queue wait=%+v want >=5ms under serialized writer pressure", x.telemetry.sendWait, x.telemetry.sendQueueWait)
			}
			close(done)
		})
		<-done
		c.Close()
		s.Close()
	})
}

// BenchmarkTelemetryBulkRPC includes actual stream parse, native admission and
// local delivery; the active Tick benchmark measures the no-change path.
func BenchmarkTelemetryBulkRPC(b *testing.B) {
	for _, size := range []int{96, 8192} {
		for _, on := range []bool{false, true} {
			b.Run(fmt.Sprintf("bytes%d/on%v", size, on), func(b *testing.B) {
				cfg := p2ptunnel.DefaultConfig()
				m, _ := p2ptunnel.New(cfg)
				m.Step(p2ptunnel.Event{Kind: p2ptunnel.Start}, 0)
				m.Step(p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{8, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}}, 0)
				var opened p2ptunnel.Action
				for _, a := range m.Step(p2ptunnel.Event{Kind: p2ptunnel.Open, Port: 4700}, 0) {
					if a.Kind == p2ptunnel.Opened {
						opened = a
					}
				}
				x := &stack{tunnel: m, f: &factory{s: &Session{statsInterval: time.Second, logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}}}
				if on {
					x.initTelemetry(0)
				}
				data := append([]byte{2, opened.Channel, byte(size), byte(size >> 8)}, bytes.Repeat([]byte{0x9a}, size)...)
				b.ReportAllocs()
				b.SetBytes(int64(size))
				b.ResetTimer()
				written := 0
				for range b.N {
					for _, a := range x.tunnelStep(p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: data}, 0) {
						if a.Kind == p2ptunnel.WriteLocal {
							written += len(a.Data)
							x.tunnelStep(p2ptunnel.Event{Kind: p2ptunnel.LocalWritten, Channel: a.Channel, Generation: a.Generation, Token: a.Token, Count: len(a.Data)}, 0)
						}
					}
				}
				b.StopTimer()
				if written != b.N*size {
					b.Fatalf("local written=%d want %d", written, b.N*size)
				}
			})
		}
	}
}
