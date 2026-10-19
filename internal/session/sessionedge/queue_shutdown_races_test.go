package sessionedge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
)

type round2Driver struct {
	*fake
	work chan func(core.Instant)
}

func (f *round2Driver) Work() <-chan func(core.Instant) { return f.work }

func TestClosedQueueRacesParentCancellation(t *testing.T) {
	for _, layer := range []session.Layer{session.Rendezvous, session.DTLS, session.RDT, session.Tunnel} {
		t.Run(fmt.Sprint(layer), func(t *testing.T) {
			lost := 0
			for range 24 {
				synctest.Test(t, func(t *testing.T) {
					var logs bytes.Buffer
					f := &round2Driver{newFake(), make(chan func(core.Instant), 1)}
					f.notices[layer] = make(chan session.Event, 1)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
					done := make(chan error, 1)
					go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, f, nil, sink) }()
					synctest.Wait()
					f.work <- func(core.Instant) { close(f.notices[layer]); cancel() }
					err := <-done
					if p, r := diagnostic.Fields(err, ""); p != diagnostic.NewTerminal(layer, "transport_failed", session.ErrTransport).Phase() || r != "transport_failed" {
						t.Errorf("closed queue owner outcome=%s/%s, want selected layer/transport_failed", p, r)
					}
					if len(sink.failed) != 1 || strings.Count(logs.String(), `"level":"ERROR"`) != 1 {
						lost++
					}
					if len(sink.failed) > 0 && sink.failed[0].Layer != layer {
						t.Errorf("origin=%d, want %d", sink.failed[0].Layer, layer)
					}
					if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
						t.Fatal("ownership changed")
					}
				})
			}
			if lost != 0 {
				t.Errorf("closed queue failure lost in %d/24 cancellation schedules", lost)
			}
		})
	}
}

func TestTerminalThenQueueCloseReportsOnce(t *testing.T) {
	for _, noticeFirst := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			var logs bytes.Buffer
			f := newFake()
			var queue *closeNotices
			factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
				l, _ := f.New(a, b)
				l.DTLS = reviewHandshake(func(ctx context.Context, _ Transport) (Transport, error) {
					tr, err := f.transport(ctx, session.DTLS)
					queue = closeQueue(tr, nil)
					return queue, err
				})
				return l, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			done := make(chan error, 1)
			go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, factory, nil, sink) }()
			synctest.Wait()
			queue.queue <- session.Event{Kind: session.Failed, Err: session.ErrTimeout}
			if noticeFirst {
				synctest.Wait()
			}
			cancel()
			<-done
			if count := strings.Count(logs.String(), `"level":"ERROR"`); count != 1 || !strings.Contains(logs.String(), `"reason":"timeout"`) {
				t.Errorf("noticeFirst=%v terminal/closure reports=%s, want one dtls/timeout", noticeFirst, &logs)
			}
			if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
				t.Fatal("ownership changed")
			}
		})
	}
}

func TestClosedResidualQueue(t *testing.T) {
	for _, capacity := range []int{0, 1} {
		ch := make(chan session.Event, capacity)
		close(ch)
		got := drainTerminal(&eventQueue{events: ch}, 1, session.DTLS)
		if !errors.Is(got, session.ErrTransport) {
			t.Errorf("closed residual capacity=%d result=%v, want transport failure", capacity, got)
		}
	}
}

func TestDrainKeepsQueueBoundAndNilDisabled(t *testing.T) {
	if got := drainTerminal(nil, 1, session.DTLS); got != nil {
		t.Errorf("nil queue result=%v, want nil", got)
	}
	q := &eventQueue{events: nil}
	if got := drainTerminal(q, 1, session.DTLS); got != nil {
		t.Errorf("disabled queue result=%v, want nil", got)
	}
	ch := make(chan session.Event, 2)
	ch <- session.Event{Kind: session.Usage}
	ch <- session.Event{Kind: session.Failed, Err: session.ErrTimeout}
	close(ch)
	if got := drainTerminal(&eventQueue{events: ch}, 1, session.DTLS); got != nil || len(ch) != 1 {
		t.Errorf("bounded drain result=%v remaining=%d, want nil and one event", got, len(ch))
	}
}
