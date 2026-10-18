package session

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

// TraceScenario uses the existing all-night budget simulator's client,
// transport-failure, release and lazy-idle cycle with its explicit PCG seed.
func TraceScenario(t *testing.T, observe func(Event, []Action, core.Instant)) Config {
	cfg := DefaultConfig()
	cfg.Idle = 2 * time.Second
	h := newHarness(t, cfg, 42)
	h.observe = observe
	h.send(Event{Kind: Start})
	h.send(Event{Kind: Client, Client: 1})
	h.run(10)
	h.send(Event{Kind: Failed, Attempt: h.m.attempt})
	h.run(core.Instant(10 * time.Second))
	h.send(Event{Kind: Client, Client: 2})
	h.run(h.q.Now() + 10)
	h.send(Event{Kind: Usage, Attempt: h.m.attempt, Layer: Tunnel, Bytes: 4096, Count: 2})
	h.send(Event{Kind: ClientGone, Attempt: h.m.attempt, Client: 1})
	h.send(Event{Kind: ClientGone, Attempt: h.m.attempt, Client: 2})
	h.run(core.Instant(20 * time.Second))
	h.finish()
	return cfg
}
