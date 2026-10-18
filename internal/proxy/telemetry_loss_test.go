package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
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

func TestIdleTelemetryLossFlush(t *testing.T) {
	var logs bytes.Buffer
	s := &Session{logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	x := &stack{f: &factory{s: s}, ctx: context.Background(), timerUpdates: make(chan time.Duration, 1)}
	x.drive(layerEvent{}, 0)
	for range debugRecordsPerInterval + 7 {
		x.transportEvent("detail")
	}
	if x.debugDropped.Load() != 7 {
		t.Fatal("fixture lost count")
	}
	if !x.telemetryOnlyTick(core.Instant(discardInterval)) {
		t.Fatal("owner wakeup not consumed")
	}
	if !strings.Contains(logs.String(), `"rule":"telemetry_detail_cap","count":7`) {
		t.Fatalf("idle interval did not report pending cap losses: %s", &logs)
	}
}

func TestTelemetryLossSessionCleanup(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")
	synctest.Test(t, func(t *testing.T) {
		var logs statsLogBuffer
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s, stacks, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		x := <-stacks
		ready := make(chan struct{})
		s.work <- func(core.Instant) {
			for range debugRecordsPerInterval + 7 {
				x.transportEvent("detail")
			}
			close(ready)
		}
		<-ready
		c.Close()
		err = s.Close()
		lost := x.debugDropped.Load()
		if lost == 0 {
			t.Fatal("fixture did not lose diagnostics")
		}
		var receipt *TelemetryLossError
		if !errors.As(err, &receipt) || receipt.Count != lost {
			t.Fatalf("close=%v, want receipt for %d telemetry losses", err, lost)
		}
		finalLogs := string(logs.take())
		if !strings.Contains(finalLogs, `"rule":"telemetry_detail_cap","count":`+fmt.Sprint(lost)) {
			t.Fatalf("missing final telemetry loss WARN: %s", finalLogs)
		}
	})
}
