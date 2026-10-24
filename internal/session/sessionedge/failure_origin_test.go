package sessionedge

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
)

func TestAttemptFailureOriginEveryPhase(t *testing.T) {
	for layer := session.Cloud; layer < session.LayerCount; layer++ {
		for _, panics := range []bool{false, true} {
			f := newFake()
			f.partial = true
			if panics {
				f.panicAt = int(layer)
			} else {
				f.fail = int(layer)
			}
			out := make(chan completion, 8)
			done := make(chan struct{})
			go attempt(context.Background(), 1, session.DefaultConfig(), f, out, done)
			<-done
			for len(out) > 0 {
				e := (<-out).event
				if e.Kind == session.Failed && e.Layer != layer {
					t.Errorf("failed phase=%v panic=%v: origin=%v", layer, panics, e.Layer)
				}
			}
		}
	}
}

type failureObserver struct {
	ChannelSink
	logger *slog.Logger
	failed []session.Event
}

func (s *failureObserver) Observe(e session.Event, _, _ session.Snapshot, _ core.Instant) {
	if e.Kind == session.Failed {
		s.failed = append(s.failed, e)
		diagnostic.Log(s.logger, "layer_failed", "session", e.Err)
	}
}

func TestConstructorFailureReportEveryPhase(t *testing.T) {
	for layer := session.Cloud; layer < session.LayerCount; layer++ {
		synctest.Test(t, func(t *testing.T) {
			var logs bytes.Buffer
			f := newFake()
			f.fail = int(layer)
			f.partial = true
			sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, f, nil, sink) }()
			synctest.Wait()
			cancel()
			<-done
			if len(sink.failed) != 1 || sink.failed[0].Layer != layer {
				t.Fatalf("constructor phase=%v, failure events=%v", layer, sink.failed)
			}
			want := [...]string{"cloud", "rendezvous", "dtls", "rdt", "tunnel"}[layer]
			var record struct{ Phase, Reason, Level string }
			if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatalf("error records: %s (%v)", &logs, err)
			}
			if record.Phase != want || record.Reason != "operation_failed" || record.Level != "ERROR" {
				t.Fatalf("constructor report=%+v, want %s/operation_failed", record, want)
			}
			if strings.Contains(logs.String(), "secret") {
				t.Fatal("raw constructor error logged")
			}
			if f.allocated.Load() != f.stopped.Load() {
				t.Fatal("constructor failure leaked lease")
			}
		})
	}
}
