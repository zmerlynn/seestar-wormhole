package sessionedge

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestDistinctEqualLabelReceipts(t *testing.T) {
	for _, sameQueue := range []bool{false, true} {
		name := "different-queues"
		if sameQueue {
			name = "same-queue"
		}
		t.Run(name, func(t *testing.T) {
			lost := 0
			counts := map[int]int{}
			var sample string
			for range 24 {
				synctest.Test(t, func(t *testing.T) {
					f := &round2Driver{newFake(), make(chan func(core.Instant), 1)}
					f.notices[session.DTLS] = make(chan session.Event, 2)
					f.notices[session.RDT] = make(chan session.Event, 2)
					var logs bytes.Buffer
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
					done := make(chan error, 1)
					go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, f, nil, sink) }()
					synctest.Wait()
					target := session.RDT
					if sameQueue {
						target = session.DTLS
					}
					f.work <- func(core.Instant) {
						f.notices[session.DTLS] <- session.Event{Kind: session.Failed, Err: diagnostic.NewTerminal(session.DTLS, "timeout", session.ErrTransport)}
						f.notices[target] <- session.Event{Kind: session.Failed, Err: diagnostic.NewTerminal(target, "timeout", session.ErrTransport)}
						cancel()
					}
					<-done
					if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 2 || !strings.Contains(logs.String(), `"reason":"timeout"`) || !strings.Contains(logs.String(), `"reason":"timeout"`) {
						lost++
						counts[n]++
						sample = logs.String()
					}
					if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
						t.Fatal("release count changed")
					}
				})
			}
			if lost != 0 {
				t.Errorf("independent failures lost in %d/24 schedules, report counts=%v; sample=%s", lost, counts, sample)
			}
		})
	}
}

func TestIndependentEqualAttachedReceipts(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	q := &eventQueue{}
	for range 2 {
		incoming := diagnostic.Wrap("dtls", "timeout", session.ErrTimeout)
		normalized := q.failed(incoming, session.DTLS)
		diagnostic.Log(logger, "layer_failed", "session", normalized)
		diagnostic.LogFinal(logger, "failed", "session", incoming, nil)
	}
	if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 2 {
		t.Fatalf("independent attached reports=%d want 2: %s", n, &logs)
	}
}
