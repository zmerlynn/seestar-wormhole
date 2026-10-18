package proxy

import (
	"bytes"
	"context"
	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func telemetryStack(t *testing.T, interval time.Duration, b *bytes.Buffer) *stack {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &Session{statsInterval: interval, logger: slog.New(slog.NewJSONHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			a.Value = slog.TimeValue(time.Unix(0, 4700123).UTC())
		}
		return a
	}}))}
	x := &stack{f: &factory{s: s}, ctx: ctx, cancel: cancel, timerUpdates: make(chan time.Duration, 1)}
	x.initTelemetry(0)
	cfg := p2ptunnel.DefaultConfig()
	cfg.HighWater = 65536 // explicit historical fixture threshold, independent of runtime default
	x.tunnel, _ = p2ptunnel.New(cfg)
	return x
}
func telemetryTunnelStep(x *stack, e p2ptunnel.Event, now core.Instant) []p2ptunnel.Action {
	aa := x.tunnelStep(e, now)
	return aa
}
func TestTelemetryPauseChurnPartialSendAndPrivacy(t *testing.T) {
	var logs bytes.Buffer
	x := telemetryStack(t, time.Second, &logs)
	startActions := telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Start}, 0)
	// literal PASS and DATA fixtures, independent of the encoder
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{8, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}}, 0)
	aa := telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Open, Port: 4700, Request: 99}, 0)
	var ch byte
	for _, a := range aa {
		if a.Kind == p2ptunnel.Opened {
			ch = a.Channel
		}
	}
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{2, ch, 1, 0, 0x9a}}, 0)
	for i := 0; i < 3; i++ {
		telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: 65537}, core.Instant(time.Duration(i)*time.Second))
		telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: 0}, core.Instant(time.Duration(i)*time.Second+250*time.Millisecond))
	}
	s := x.telemetry.channels[ch]
	if s.pause != 3 || s.resume != 3 || s.paused != 750*time.Millisecond {
		t.Fatalf("flow counters=%+v", s)
	}
	var send p2ptunnel.Action
	for _, a := range startActions {
		if a.Kind == p2ptunnel.Send {
			send = a
		}
	}
	// First handoff is partial: no control is sent until its complete frame is accepted.
	for iteration := 0; len(send.Data) > 0 && iteration < 32; iteration++ {
		count := len(send.Data)
		if iteration == 0 {
			count = 1
		}
		aa := telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: send.Token, Count: count}, core.Instant(3*time.Second))
		send = p2ptunnel.Action{}
		for _, a := range aa {
			if a.Kind == p2ptunnel.Send {
				send = a
			}
		}
	}
	if x.telemetry.channels[0].wakeSent != 0 {
		t.Fatal("LOGIN opcode counted as WAKE")
	}
	if got := x.telemetry.channels[ch]; got.pauseSent != 0 || got.resumeSent != 0 {
		t.Fatalf("sent counts=%+v", got)
	}
	// Unaccepted PAUSE/RESUME pairs coalesce; stale completions must not recount on partial/stale completions.
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: 987654321, Count: 1}, core.Instant(3*time.Second))
	if x.telemetry.channels[ch].pause != 3 {
		t.Fatal("stale completion recounted flow")
	}
	for i := 0; i < 1000; i++ {
		x.telemetryEvent("fixture", slog.Int("channel", int(ch)))
	}
	x.stats(core.Instant(4 * time.Second))
	if x.telemetry.debugDropped == 0 {
		t.Fatal("debug events not capped")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		var fields map[string]any
		if err := json.Unmarshal(line, &fields, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		allowed := " time level msg scope channel event reason pause_delta resume_delta wake_delta decision_q decision_q_exact pause_sent resume_sent wake_sent pause_queued resume_queued wake_queued paused_ms rx_bytes rx_backlog_age_ms rx_progress_age_ms delivery_bytes delivery_age_ms admitted_bytes delivered_bytes discarded_bytes admitted_packets high_water low_water decision_q_max rx_peak_bytes paused accepted_paused wake_pending pending_control handoff_control handoff_accepted_bytes pressure_q attributed_rx_bytes pause_received resume_received wake_received local_read_state capacity_rejects capacity_reclaims debug_dropped owner_wait_count owner_wait_total_ms owner_wait_max_ms edge_send_wait_count edge_send_wait_total_ms edge_send_wait_max_ms edge_send_queue_wait_count edge_send_queue_wait_total_ms edge_send_queue_wait_max_ms edge_send_queue_bytes owner_queue_count layer_queue_bytes layer_queue_age_ms ingress_capacity_rejects "
		for key := range fields {
			if !strings.Contains(allowed, " "+key+" ") {
				t.Fatalf("unapproved key %s", key)
			}
		}
		// Handler timestamps may contain arbitrary numeric identifier substrings.
		// Privacy belongs to the controlled telemetry attributes, whose keys were
		// checked above; the fixture timestamp deliberately contains the port digits.
		delete(fields, slog.TimeKey)
		attributes, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(attributes, []byte("987654321")) || bytes.Contains(attributes, []byte("4700")) {
			t.Fatal("identity in telemetry attributes")
		}
	}
}
func TestTelemetryDisabledEdgeAllocations(t *testing.T) {
	var logs bytes.Buffer
	x := telemetryStack(t, 0, &logs)
	if x.telemetry != nil {
		t.Fatal("disabled telemetry allocated state")
	}
	n := testing.AllocsPerRun(1000, func() { x.initTelemetry(0); x.stats(0); x.telemetryEvent("fixture") })
	if n != 0 || logs.Len() != 0 {
		t.Fatalf("disabled allocs=%v logs=%d", n, logs.Len())
	}
}

func TestTelemetryComposedPeriodicAndWaits(t *testing.T) {

	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s, stacks, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{StatsInterval: time.Second, Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		if err := exchange(c.(*stream), 1122334455, 200); err != nil {
			t.Fatal(err)
		}
		x := <-stacks
		timer := time.NewTimer(3 * time.Second) // virtual time exercises idle periodic wakeup
		<-timer.C
		done := make(chan struct{})
		x.post(func(now core.Instant) {
			if x.telemetry.ownerWait.count == 0 || x.telemetry.sendWait.count == 0 || x.telemetry.sendQueueWait.count == 0 {
				t.Error("missing edge wait observations")
			}
			close(done)
		})
		<-done
		c.Close()
		s.Close()
		if !strings.Contains(logs.String(), `"msg":"rdt_stats"`) || !strings.Contains(logs.String(), `"msg":"tunnel_stats"`) {
			t.Fatal("missing periodic records")
		}
		for _, secret := range []string{"token-sentinel", "source-sentinel", "sim-user", "sim-pass", "sim-allocation", "EXAMPLEUID", "get_device_state", "1122334455", strings.Repeat("x", 40)} {
			if strings.Contains(logs.String(), secret) {
				t.Fatalf("leak %s", secret)
			}
		}
	})
}

func TestTelemetryWakeQueueAndCompletion(t *testing.T) {
	var logs bytes.Buffer
	x := telemetryStack(t, time.Second, &logs)
	aa := telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Start}, 0)
	var send p2ptunnel.Action
	for _, a := range aa {
		if a.Kind == p2ptunnel.Send {
			send = a
		}
	}
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{8, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}}, 0)
	aa = telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Open, Port: 445, Request: 1}, 0)
	var opened p2ptunnel.Action
	for _, a := range aa {
		if a.Kind == p2ptunnel.Opened {
			opened = a
		}
	}
	for len(send.Data) > 0 {
		aa = telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: send.Token, Count: len(send.Data)}, 0)
		send = p2ptunnel.Action{}
		for _, a := range aa {
			if a.Kind == p2ptunnel.Send {
				send = a
			}
		}
	}
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: 65537}, 0)
	data := append([]byte{2, opened.Channel, 0, 32}, bytes.Repeat([]byte{0xde}, 8192)...)
	var write p2ptunnel.Action
	for range 3 {
		aa = telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: data}, 0)
		for _, a := range aa {
			if a.Kind == p2ptunnel.WriteLocal {
				write = a
			}
			if a.Kind == p2ptunnel.Send {
				send = a
			}
		}
	}
	// Complete PAUSE before the drain opportunity; it is stored outside DATA TX.
	if len(send.Data) != 4 || send.Data[0] != 5 {
		t.Fatalf("PAUSE wire=%x", send.Data)
	}
	telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: send.Token, Count: len(send.Data)}, 0)
	send = p2ptunnel.Action{}
	for i := 0; i < 2; i++ {
		aa = telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.LocalWritten, Channel: write.Channel, Generation: write.Generation, Token: write.Token, Count: len(write.Data)}, core.Instant(time.Millisecond))
		for _, a := range aa {
			if a.Kind == p2ptunnel.WriteLocal {
				write = a
			}
			if a.Kind == p2ptunnel.Send {
				send = a
			}
		}
	}
	ch := x.telemetry.channels[opened.Channel]
	if ch.wake != 1 || ch.admitted != 24576 || ch.delivery != 8192 {
		t.Fatalf("wake/delivery counters=%+v", ch)
	}
	for iteration := 0; len(send.Data) > 0 && iteration < 32; iteration++ {
		aa = telemetryTunnelStep(x, p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: send.Token, Count: len(send.Data)}, core.Instant(2*time.Millisecond))
		send = p2ptunnel.Action{}
		for _, a := range aa {
			if a.Kind == p2ptunnel.Send {
				send = a
			}
		}
	}
	if x.telemetry.channels[opened.Channel].wakeSent != 1 {
		t.Fatal("WAKE handoff not counted exactly once")
	}
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
		var record map[string]any
		if err := json.Unmarshal(line, &record, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		if record["msg"] == "tunnel_flow" {
			if record["channel"] != float64(opened.Channel) {
				t.Fatalf("flow attributed to wrong channel: %v", record)
			}
			records = append(records, record)
		}
	}
	if len(records) != 2 {
		t.Fatalf("flow records=%v want exactly PAUSE and WAKE", records)
	}
	checkFlowRecord(t, records[0], 5, "high_water", 73729, true, 1, 0, 0)
	checkFlowRecord(t, records[1], 7, "drain_opportunity", 73729, true, 0, 0, 1)
}

func BenchmarkTelemetryEdgeDisabled(b *testing.B) {
	for _, wrapper := range []bool{false, true} {
		name := "core"
		if wrapper {
			name = "edge_off"
		}
		b.Run(name, func(b *testing.B) {
			m, _ := p2ptunnel.New(p2ptunnel.DefaultConfig())
			x := &stack{tunnel: m}
			e := p2ptunnel.Event{Kind: p2ptunnel.Tick}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if wrapper {
					x.tunnelStep(e, 0)
				} else {
					m.Step(e, 0)
				}
			}
		})
	}
}

func TestTelemetryOnlyTickDoesNotAdvanceProtocol(t *testing.T) {
	var logs bytes.Buffer
	x := telemetryStack(t, time.Second, &logs)
	cfg := runtimeRDTConfig()
	cfg.Telemetry = true
	x.reliable, _ = rdt.New(cfg)
	x.reliable.Step(rdt.Event{Kind: rdt.Start}, 0)
	// No protocol deadline is dispatched here: a stats-only callback must sample
	// rather than inject Tick and trigger this core's overdue heartbeat.
	if !x.telemetryOnlyTick(core.Instant(time.Second)) || x.failed {
		t.Fatal("telemetry tick advanced protocol")
	}
	if x.telemetry.next != core.Instant(2*time.Second) {
		t.Fatal("telemetry timer was not rearmed")
	}
	x.deadlines[1] = core.Instant(2 * time.Second)
	if x.telemetryOnlyTick(core.Instant(2 * time.Second)) {
		t.Fatal("stats suppressed an actual protocol deadline")
	}
}
