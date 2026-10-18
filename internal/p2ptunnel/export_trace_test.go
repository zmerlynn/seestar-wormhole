package p2ptunnel

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
)

// TraceScenario observes the existing fragmented paired lifecycle, partial
// local writes and simultaneous EOF scenario, including every local completion.
func TraceScenario(t *testing.T, observe func(int, Event, []Action, core.Instant)) {
	runPairedLifecycle(t, observe)
}
