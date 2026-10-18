package proxy

import (
	"bytes"
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestTelemetryCapWarning(t *testing.T) {
	for _, mode := range []string{"interval", "attempt", "shutdown"} {
		metrics := mode == "interval"
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			s := &Session{logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), statsInterval: time.Second}
			x := &stack{f: &factory{s: s}, ctx: context.Background()}
			if metrics {
				x.initTelemetry(0)
			}
			if mode == "shutdown" {
				done := make(chan struct{})
				go func() {
					defer close(done)
					for range 10000 {
						x.reportTelemetryLoss()
					}
				}()
				for range 10000 {
					x.transportEvent("detail")
				}
				<-done
				x.reportTelemetryLoss()
				if !strings.Contains(logs.String(), `"count":9936`) || !strings.Contains(logs.String(), `"level":"WARN"`) {
					t.Fatalf("missing final loss count: %s", &logs)
				}
				return
			}
			for range debugRecordsPerInterval + 7 {
				x.transportEvent("detail")
			}
			if metrics {
				x.stats(core.Instant(time.Second))
			} else {
				x.reportTelemetryLoss()
			}
			got := logs.String()
			want := 1
			if !metrics {
				want = 2
			}
			if strings.Count(got, `"rule":"telemetry_detail_cap"`) != want || !strings.Contains(got, `"count":7`) || !strings.Contains(got, `"level":"WARN"`) {
				t.Fatalf("missing cap summary: %s", got)
			}
			before := logs.Len()
			x.reportTelemetryLoss()
			if logs.Len() != before {
				t.Fatal("unchanged count reported twice")
			}
		})
	}
}
