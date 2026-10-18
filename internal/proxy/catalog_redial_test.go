package proxy

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestAllocatorRechecksPairForEveryAttempt(t *testing.T) {
	tr := &pairAllocationCloud{t: t, detail: `{"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic"}}`}
	var logs bytes.Buffer
	c, err := newFixtureClient(t, cloudhttp.Config{Transport: tr, Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a := allocator{c, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
	l, err := a.Allocate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The next attempt sees a refusal for another model.
	tr.detail = `{"code":300404,"data":{"deviceSn":"sim","deviceModel":"S30","deviceStatus":4,"ssid":"synthetic"}}`
	l, err = a.Allocate(t.Context())
	phase, reason := diagnostic.Fields(err, "")
	if l != nil || !errors.Is(err, session.ErrUncertain) || phase != "cloud" || reason != "inconsistent server answer: detail identity differs from requested pair" {
		t.Fatalf("second lease=%v error=%v diagnostic=%s/%s detail count=%d, want inconsistent detail refusal; logs=%s", l, err, phase, reason, tr.details.Load(), &logs)
	}
	if tr.details.Load() != 2 || tr.allocs.Load() != 1 || tr.stops.Load() != 1 {
		t.Fatalf("detail/online-link/stop=%d/%d/%d, want 2/1/1", tr.details.Load(), tr.allocs.Load(), tr.stops.Load())
	}
}
