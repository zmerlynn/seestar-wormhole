package p2ptunnel_test

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord/tracetest"
	"testing"
)

func TestRecordedSimulatorReplay(t *testing.T) {
	capture := tracetest.New(t, 4700)
	var inputs [2][]p2ptunnel.Event
	p2ptunnel.TraceScenario(t, func(side int, e p2ptunnel.Event, aa []p2ptunnel.Action, now core.Instant) {
		e.Data = bytes.Clone(e.Data)
		inputs[side] = append(inputs[side], e)
		capture.Step("tunnel", uint64(side+1), e, aa, now)
	})
	records := capture.Fixture("testdata/trace-lifecycle.jsonl")
	for side := range inputs {
		cfg := p2ptunnel.DefaultConfig()
		if side == 1 {
			cfg.Role = p2ptunnel.Server
			cfg.VerifyPorts = true
			cfg.AllowedPorts = []uint16{4700}
		}
		m, err := p2ptunnel.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		index := 0
		hydrate := func(r tracerecord.Record) (p2ptunnel.Event, error) {
			e := inputs[side][index]
			index++
			e.Kind = p2ptunnel.EventKind(r.Input.Kind)
			e.Channel = r.Input.Channel
			e.Generation = r.Input.Generation
			e.Token = r.Input.Token
			e.Request = r.Input.Request
			e.Count = r.Input.Count
			if r.Input.PortAlias == "" {
				e.Port = r.Input.Port
			}
			e.Failed = r.Input.Flag
			return e, nil
		}
		if err := tracerecord.Replay(records, "tunnel", uint64(side+1), hydrate, m.Step); err != nil {
			t.Fatal(err)
		}
		if index != len(inputs[side]) {
			t.Fatal("missed inputs")
		}
	}
}
