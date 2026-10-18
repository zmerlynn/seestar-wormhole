package proxy

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"reflect"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
)

// Run identical events through an observed edge and a native core. Comparing
// complete actions and native snapshots also protects handoff/accounting policy.
type flowDecisionFixture struct {
	t      *testing.T
	x      *stack
	native *p2ptunnel.Machine
	logs   bytes.Buffer
	send   p2ptunnel.Action
	a, b   byte
}

func newFlowDecisionFixture(t *testing.T) *flowDecisionFixture {
	t.Helper()
	f := &flowDecisionFixture{t: t}
	f.x = telemetryStack(t, time.Second, &f.logs)
	cfg := p2ptunnel.DefaultConfig()
	cfg.HighWater = 100
	var err error
	f.x.tunnel, err = p2ptunnel.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.native, err = p2ptunnel.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.native.EnableTelemetry()
	f.step(p2ptunnel.Event{Kind: p2ptunnel.Start})
	f.step(p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{8, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}})
	for _, id := range []*byte{&f.a, &f.b} {
		for _, a := range f.step(p2ptunnel.Event{Kind: p2ptunnel.Open, Port: 445}) {
			if a.Kind == p2ptunnel.Opened {
				*id = a.Channel
			}
		}
	}
	if f.a == f.b {
		t.Fatal("fixture needs two distinct channels")
	}
	return f
}

func (f *flowDecisionFixture) step(e p2ptunnel.Event) []p2ptunnel.Action {
	f.t.Helper()
	got := f.x.tunnelStep(e, 0)
	want := f.native.Step(e, 0)
	if !reflect.DeepEqual(got, want) {
		f.t.Fatalf("event %v actions=%+v want native %+v", e.Kind, got, want)
	}
	if f.x.tunnel.Usage() != f.native.Usage() {
		f.t.Fatal("native usage changed")
	}
	for i := range 256 {
		if f.x.tunnel.Telemetry(byte(i), 0) != f.native.Telemetry(byte(i), 0) {
			f.t.Fatalf("channel %d accounting changed", i)
		}
	}
	if e.Kind == p2ptunnel.Sent && e.Token == f.send.Token {
		f.send = p2ptunnel.Action{}
	}
	for _, a := range got {
		if a.Kind == p2ptunnel.Send {
			f.send = a
		}
	}
	return got
}

func (f *flowDecisionFixture) drain() {
	f.t.Helper()
	for i := 0; len(f.send.Data) > 0; i++ {
		if i == 32 {
			f.t.Fatal("send drain did not finish")
		}
		f.step(p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: f.send.Token, Count: len(f.send.Data)})
	}
}

func (f *flowDecisionFixture) records(id byte) []map[string]any {
	f.t.Helper()
	var out []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(f.logs.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal(line, &v, json.MatchCaseInsensitiveNames(true)); err != nil {
			f.t.Fatal(err)
		}
		if v["msg"] == "tunnel_flow" && v["channel"] == float64(id) {
			out = append(out, v)
		}
	}
	return out
}

func checkFlowRecord(t *testing.T, got map[string]any, event int, reason string, q int, exact bool, pause, resume, wake int) {
	t.Helper()
	for key, want := range map[string]any{
		"event": float64(event), "reason": reason, "decision_q": float64(q), "decision_q_exact": exact,
		"pause_delta": float64(pause), "resume_delta": float64(resume), "wake_delta": float64(wake),
	} {
		if got[key] != want {
			t.Errorf("flow %s=%v want %v; record=%v", key, got[key], want, got)
		}
	}
}

func TestTelemetryDecisionMixedStep(t *testing.T) {
	f := newFlowDecisionFixture(t)
	f.step(p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: 200})
	// Literal DATA A=1 then B=200 in one Stream: A pauses at Q=201,
	// then resumes at Q=1 after attribution moves the shared pressure to B.
	data := append([]byte{2, f.a, 1, 0, 0x9a, 2, f.b, 200, 0}, bytes.Repeat([]byte{0x9a}, 200)...)
	f.step(p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: data})
	v := f.x.tunnel.Telemetry(f.a, 0)
	if v.Pause != 1 || v.Resume != 1 || v.DecisionQ != 1 || v.DecisionQMax != 201 || v.AdmittedBytes != 1 || v.IsPaused {
		t.Fatalf("mixed fixture accounting=%+v", v)
	}
	records := f.records(f.a)
	if len(records) != 1 {
		t.Fatalf("mixed records=%v want one bounded summary", records)
	}
	checkFlowRecord(t, records[0], 0, "mixed_decisions", 1, false, 1, 1, 0)
	records = f.records(f.b)
	if len(records) != 1 {
		t.Fatalf("B records=%v want one PAUSE", records)
	}
	checkFlowRecord(t, records[0], 5, "high_water", 399, true, 1, 0, 0)
	// A's unsent PAUSE/RESUME cancel while startup remains outstanding.
	// Only B's literal PAUSE can reach the handoff.
	var controls [][]byte
	for len(f.send.Data) > 0 {
		if len(f.send.Data) == 4 {
			controls = append(controls, bytes.Clone(f.send.Data))
		}
		f.step(p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: f.send.Token, Count: len(f.send.Data)})
	}
	if !reflect.DeepEqual(controls, [][]byte{{5, f.b, 0, 0}}) {
		t.Fatalf("coalesced wire=%x want only B PAUSE", controls)
	}
	v = f.x.tunnel.Telemetry(f.a, 0)
	if v.PauseSent != 0 || v.ResumeSent != 0 || len(f.records(f.a)) != 1 {
		t.Fatalf("coalesced completion recounted decision: %+v", v)
	}
}

func TestTelemetryDecisionIndividualAndPartial(t *testing.T) {
	f := newFlowDecisionFixture(t)
	f.drain()
	f.step(p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{2, f.a, 1, 0, 0x9a}})
	f.step(p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: 200})
	if !bytes.Equal(f.send.Data, []byte{5, f.a, 0, 0}) {
		t.Fatalf("PAUSE wire=%x", f.send.Data)
	}
	f.step(p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: f.send.Token, Count: 1})
	v := f.x.tunnel.Telemetry(f.a, 0)
	if v.PauseSent != 0 || v.HandoffAcceptedBytes != 1 {
		t.Fatalf("partial PAUSE=%+v", v)
	}
	f.step(p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: 0})
	if !bytes.Equal(f.send.Data, []byte{f.a, 0, 0}) {
		t.Fatalf("immutable PAUSE suffix=%x", f.send.Data)
	}
	f.step(p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: 987654321, Count: 3})
	f.step(p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: f.send.Token, Count: 3})
	if !bytes.Equal(f.send.Data, []byte{6, f.a, 0, 0}) {
		t.Fatalf("RESUME wire=%x", f.send.Data)
	}
	f.drain()
	v = f.x.tunnel.Telemetry(f.a, 0)
	if v.Pause != 1 || v.Resume != 1 || v.PauseSent != 1 || v.ResumeSent != 1 || v.DecisionQMax != 201 || v.AdmittedBytes != 1 {
		t.Fatalf("individual/partial accounting=%+v", v)
	}
	records := f.records(f.a)
	if len(records) != 2 {
		t.Fatalf("individual records=%v want exactly PAUSE, RESUME", records)
	}
	checkFlowRecord(t, records[0], 5, "high_water", 201, true, 1, 0, 0)
	checkFlowRecord(t, records[1], 6, "low_water", 1, true, 0, 1, 0)
}

func TestTelemetryDecisionSummaryBudget(t *testing.T) {
	f := newFlowDecisionFixture(t)
	f.step(p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{2, f.a, 1, 0, 0x9a}})
	for range 40 {
		f.step(p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: 200})
		f.step(p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: 0})
	}
	if got := len(f.records(f.a)); got != 64 {
		t.Fatalf("flow records=%d want 64", got)
	}
	if got := f.x.telemetry.debugDropped; got != 16 {
		t.Fatalf("suppressed records=%d want 16", got)
	}
	// Suppression must still advance the edge baseline: the next interval's
	// first decision has a delta of one, not a backlog of dropped transitions.
	f.x.stats(1e9)
	f.logs.Reset()
	f.step(p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: 200})
	records := f.records(f.a)
	if len(records) != 1 {
		t.Fatalf("next interval records=%v", records)
	}
	checkFlowRecord(t, records[0], 5, "high_water", 201, true, 1, 0, 0)
}
