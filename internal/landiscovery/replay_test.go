package landiscovery_test

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord/tracetest"
	"testing"
	"time"
)

func TestRecordedLANRateReplay(t *testing.T) {
	newMachine := func() *landiscovery.Machine {
		m, _, err := landiscovery.New("fixture-serial", "fixture-name", "fixture-model")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := newMachine()
	capture := tracetest.New(t)
	scan := []byte(`{"id":1,"method":"scan_iscope"}`)
	type event struct {
		data []byte
		now  time.Duration
	}
	// Same burst/refill/regression cases as TestRateLimit; raw JSON is synthetic.
	events := []event{{scan, 0}, {scan, 0}, {scan, 0}, {scan, 0}, {scan, time.Second - 1}, {[]byte(`{`), time.Second}, {scan, time.Second}, {scan, 0}, {scan, 2 * time.Second}}
	for _, e := range events {
		b := m.Step(e.data, e.now)
		capture.Step("lan", 1, tracerecord.EdgeEvent{Kind: 0, Data: e.data, Type: 1}, []tracerecord.EdgeEvent{{Kind: 1, Data: b, Type: 1, Present: b != nil}}, core.Instant(e.now))
	}
	rr := capture.Fixture("testdata/trace-rate.jsonl")
	fresh := newMachine()
	index := 0
	hydrate := func(r tracerecord.Record) (tracerecord.EdgeEvent, error) {
		e := tracerecord.EdgeEvent{Kind: r.Input.Kind, Type: r.Input.Type, Data: events[index].data}
		index++
		return e, nil
	}
	step := func(e tracerecord.EdgeEvent, now core.Instant) []tracerecord.EdgeEvent {
		b := fresh.Step(e.Data, time.Duration(now))
		return []tracerecord.EdgeEvent{{Kind: 1, Type: 1, Data: b, Present: b != nil}}
	}
	if err := tracerecord.Replay(rr, "lan", 1, hydrate, step); err != nil {
		t.Fatal(err)
	}
}
