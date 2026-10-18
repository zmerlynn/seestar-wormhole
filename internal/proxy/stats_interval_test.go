package proxy

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestStatsIntervalOverflow(t *testing.T) {
	var logs bytes.Buffer
	x := telemetryStack(t, 0, &logs)
	x.f.s.statsInterval = time.Duration(1<<63 - 1)
	x.initTelemetry(core.Instant(time.Second))
	for range 3 {
		x.stats(core.Instant(time.Second))
	}
	if logs.Len() != 0 {
		t.Fatalf("max valid positive interval emitted %d premature records at 1s; next=%d", bytes.Count(logs.Bytes(), []byte{'\n'}), x.telemetry.next)
	}
}
