package iotc_test

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord/tracetest"
	"testing"
)

func TestRecordedSimulatorReplay(t *testing.T) {
	for _, mode := range []string{"direct", "relay"} {
		t.Run(mode, func(t *testing.T) {
			capture := tracetest.New(t)
			var inputs []iotc.Event
			cfg := iotc.TraceScenario(t, mode, func(e iotc.Event, aa []iotc.Action, now core.Instant) {
				e.Data = bytes.Clone(e.Data)
				e.Addresses = append(e.Addresses[:0:0], e.Addresses...)
				inputs = append(inputs, e)
				capture.Step("iotc", 1, e, aa, now)
			})
			records := capture.Fixture("testdata/trace-" + mode + ".jsonl")
			m, err := iotc.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			index := 0
			hydrate := func(r tracerecord.Record) (iotc.Event, error) {
				e := inputs[index]
				index++
				// Addresses, material and authenticated bytes are synthetic simulator
				// values, supplied out of band; scalar inputs come from the trace.
				e.Kind = iotc.EventKind(r.Input.Kind)
				e.Attempt = r.Input.Attempt
				e.Token = r.Input.Token
				e.Link.Transport = iotc.Transport(r.Input.Transport)
				return e, nil
			}
			if err := tracerecord.Replay(records, "iotc", 1, hydrate, m.Step); err != nil {
				t.Fatal(err)
			}
			if index != len(inputs) {
				t.Fatal("missed inputs")
			}
		})
	}
}
