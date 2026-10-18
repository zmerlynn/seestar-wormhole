package rdt

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"testing"
	"time"
)

// TraceScenario reuses the deterministic wire/telemetry simulator, including
// its initial creation flights. The callback observes before edge execution.
func TraceScenario(t testing.TB, link sim.Link, observe func(int, Event, []Action, core.Instant)) {
	p := newPairObserved(t, 1901, link, func(c *Config) { c.InitialWindow = 16; c.MinimumWindow = 8 }, observe)
	p.run(time.Second)
	input := bytes.Repeat([]byte{0x9a}, 8196)
	p.step(0, Event{Kind: Write, Data: input}, p.clock.Now())
	p.run(5 * time.Second)
	if !bytes.Equal(p.output[1], input) || len(p.failures) != 0 {
		t.Fatalf("simulator output %q failures %v", p.output[1], p.failures)
	}
	if link.LossPPM > 0 && p.network.Dropped == 0 {
		t.Fatal("loss scenario had no loss")
	}
	if link.DuplicatePPM > 0 && p.network.Duplicated == 0 {
		t.Fatal("duplicate scenario had no duplication")
	}
}
