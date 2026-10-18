package rdt_test

import (
	"bytes"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord/tracetest"
	"testing"
	"time"
)

func TestRecordedSimulatorReplay(t *testing.T) {
	for _, tc := range []struct {
		name string
		link sim.Link
	}{
		{"ordered", sim.Link{MTU: 1388, Delay: time.Millisecond}},
		{"reorder_duplicate", sim.Link{MTU: 1388, Delay: time.Millisecond, Jitter: 30 * time.Millisecond, DuplicatePPM: 250000}},
		{"loss", sim.Link{MTU: 1388, Delay: time.Millisecond, LossPPM: 150000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := tracetest.New(t)
			// Public synthetic control payloads (SACK) are retained only in the test
			// resolver. Neither these nor application bytes go into the JSON Lines file.
			var events [2][]rdt.Event
			rdt.TraceScenario(t, tc.link, func(i int, e rdt.Event, aa []rdt.Action, now core.Instant) {
				e.Data = bytes.Clone(e.Data)
				e.Packet.Payload = bytes.Clone(e.Packet.Payload)
				events[i] = append(events[i], e)
				capture.Step("rdt", uint64(i+1), e, aa, now)
			})
			records := capture.Fixture("testdata/trace-" + tc.name + ".jsonl")
			for i := range events {
				cfg := rdt.DefaultConfig()
				cfg.LocalID = byte(7 + 2*i)
				cfg.InitialWindow = 16
				cfg.MinimumWindow = 8
				m, err := rdt.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				index := 0
				hydrate := func(r tracerecord.Record) (rdt.Event, error) {
					if index >= len(events[i]) {
						return rdt.Event{}, errors.New("missing synthetic input")
					}
					e := events[i][index]
					index++
					// Drive scalar input fields from the recorded input, resolving only the
					// erased payload and byte-sized wire ID from the public simulator fixture.
					e.Kind = rdt.EventKind(r.Input.Kind)
					e.Count = r.Input.Count
					e.Token = r.Input.Token
					if e.Kind == rdt.Receive {
						e.Packet.Type = rdt.Type(r.Input.Type)
						e.Packet.Sequence = r.Input.Sequence
						e.Packet.Version = r.Input.Version
						e.Packet.Transmissions = r.Input.Transmissions
					}
					return e, nil
				}
				if err := tracerecord.Replay(records, "rdt", uint64(i+1), hydrate, m.Step); err != nil {
					t.Fatal(err)
				}
				if index != len(events[i]) {
					t.Fatal("replay missed inputs")
				}
			}
		})
	}
}
