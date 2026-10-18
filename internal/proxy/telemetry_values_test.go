package proxy

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
)

func TestTelemetryLayerQueueAgeValues(t *testing.T) {
	var logs bytes.Buffer
	x := telemetryStack(t, 10*time.Millisecond, &logs)
	x.tunnel = nil
	// A pending wire effect blocks IOTC dispatch while other layers can progress.
	x.effects = []iotc.Action{{}}
	enqueue := func(ms int, e layerEvent) {
		x.now = core.Instant(time.Duration(ms) * time.Millisecond)
		x.enqueue(e)
	}
	sample := func(ms, count int, age time.Duration) {
		t.Helper()
		now := core.Instant(time.Duration(ms) * time.Millisecond)
		if got := len(x.queue); got != count {
			t.Fatalf("at %dms layer queue count=%d want %d", ms, got, count)
		}
		if got := x.ownerQueueAge(now); got != age {
			t.Fatalf("at %dms queue age=%v want %v", ms, got, age)
		}
		logs.Reset()
		x.stats(now)
		var record map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		if record["msg"] != "transport_stats" || record["layer_queue_age_ms"] != float64(age/time.Millisecond) {
			t.Fatalf("at %dms record=%v want queue count=%d age=%v", ms, record, count, age)
		}
	}
	enqueue(100, layerEvent{r: &rdt.Event{Kind: rdt.Tick}})
	enqueue(140, layerEvent{i: &iotc.Event{Kind: iotc.Tick}})
	enqueue(180, layerEvent{r: &rdt.Event{Kind: rdt.Tick}}) // coalesced; do not renew age
	sample(220, 2, 120*time.Millisecond)
	x.drive(layerEvent{}, core.Instant(230*time.Millisecond)) // remove head; leave blocked IOTC
	sample(240, 1, 100*time.Millisecond)
	enqueue(260, layerEvent{r: &rdt.Event{Kind: rdt.Tick}})
	x.drive(layerEvent{}, core.Instant(280*time.Millisecond)) // remove non-head
	sample(300, 1, 160*time.Millisecond)
	x.effects = nil
	x.drive(layerEvent{}, core.Instant(320*time.Millisecond))
	sample(340, 0, 0)
	enqueue(400, layerEvent{r: &rdt.Event{Kind: rdt.Tick}})
	sample(450, 1, 50*time.Millisecond)
	x.drive(layerEvent{}, core.Instant(460*time.Millisecond))
	sample(480, 0, 0)
}
