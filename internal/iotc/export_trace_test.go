package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

// TraceScenario records the existing NAT-outcome simulator with seeded loss,
// deterministic material and synthetic authenticated crypto completions.
func TraceScenario(t *testing.T, mode string, observe func(Event, []Action, core.Instant)) Config {
	h := newHarness(t, mode, 200000)
	cfg := h.m.cfg
	h.observe = observe
	h.dropACK = true
	h.step(Event{Kind: Start})
	h.run(10 * time.Second)
	if h.ready != 1 {
		t.Fatal("not ready", h.trace)
	}
	h.step(Event{Kind: Write, Data: []byte{0x17, 0xfe, 0xfd, 1, 2}})
	h.run(time.Second)
	h.step(Event{Kind: Cancel})
	h.run(time.Second)
	if h.disposed != 1 {
		t.Fatal("not disposed")
	}
	return cfg
}
