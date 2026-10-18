package sessionedge

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"unsafe"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestTwoResidualCausesPreserveOutcomes(t *testing.T) {
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
						f.notices[target] <- session.Event{Kind: session.Failed, Err: diagnostic.NewTerminal(target, "invalid_frame", session.ErrTransport)}
						cancel()
					}
					<-done
					if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 2 || !strings.Contains(logs.String(), `"reason":"timeout"`) || !strings.Contains(logs.String(), `"reason":"invalid_frame"`) {
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

func TestResidualBatchFitsCompletionGuarantee(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := session.DefaultConfig()
		f := newFake()
		for layer := session.Rendezvous; layer < session.LayerCount; layer++ {
			f.notices[layer] = make(chan session.Event, 2)
			f.notices[layer] <- session.Event{Kind: session.Failed, Err: diagnostic.NewTerminal(layer, "timeout", session.ErrTransport)}
			f.notices[layer] <- session.Event{Kind: session.Failed, Err: diagnostic.NewTerminal(layer, "invalid_frame", session.ErrTransport)}
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		out, done := make(chan completion, 8), make(chan struct{})
		go attempt(ctx, 1, cfg, f, out, done)
		synctest.Wait()
		cancel()
		<-done // Teardown must finish with no completion consumer.
		if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
			t.Fatal("lease ownership changed")
		}
		m, err := session.New(cfg, rng{})
		if err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
		o := &owner{cfg: cfg, machine: m, sink: sink, results: out}
		before := m.Snapshot()
		for range len(out) {
			r := <-out
			for batch := r.residual(); batch != nil; batch = batch.next {
				layer, outcomes := batch.layer, batch.outcomes
				retained := int(unsafe.Sizeof(*batch)) + cap(outcomes)*int(unsafe.Sizeof(error(nil)))
				if retained > max(1, cap(f.notices[layer]))*int(unsafe.Sizeof(session.Event{})) {
					t.Errorf("layer %d residual storage %d exceeds replaced events", layer, retained)
				}
				if len(outcomes) > cap(f.notices[layer]) || cap(outcomes) > max(1, cap(f.notices[layer])) {
					t.Errorf("layer %d batch len/cap=%d/%d exceeds queue reservation %d", layer, len(outcomes), cap(outcomes), cap(f.notices[layer]))
				}
			}
			// Put each completion back for the existing owner join path.
			out <- r
			if len(out) == cap(out) {
				t.Fatal("completion capacity exhausted")
			}
		}
		o.collectCleanup()
		if got := m.Snapshot(); got != before {
			t.Fatalf("observational drain changed machine: got %+v, want %+v", got, before)
		}
		phase, reason := diagnostic.Fields(o.failureErr, "")
		lastPhase, lastReason := diagnostic.Fields(sink.failed[len(sink.failed)-1].Err, "")
		if lastPhase != phase || lastReason != reason {
			t.Fatalf("observer primary=%s/%s, want selected %s/%s", lastPhase, lastReason, phase, reason)
		}
		if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 8 {
			t.Fatalf("drained reports=%d, want 8: %s", n, &logs)
		}
	})
}
