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

func TestMultipleJoinedTypedConstructorCancellation(t *testing.T) {
	for layer := session.Cloud; layer < session.LayerCount; layer++ {
		t.Run(fmt.Sprint(layer), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFake()
				entered := make(chan struct{})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				// A timeout is observed before cancellation; only publication waits.
				fail := func(c context.Context) error {
					cause := errors.Join(diagnostic.NewTerminal(layer, "timeout", session.ErrTransport), diagnostic.NewTerminal(layer, "capacity", session.ErrTransport))
					close(entered)
					<-c.Done()
					return errors.Join(cause, c.Err())
				}
				factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
					l, _ := f.New(a, b)
					switch layer {
					case session.Cloud:
						l.Cloud = allocateFunc(func(c context.Context) (Lease, error) { return nil, fail(c) })
					case session.Rendezvous:
						l.Rendezvous = round2Rendezvous(func(c context.Context, _ Lease) (Transport, error) { return nil, fail(c) })
					case session.DTLS:
						l.DTLS = round2Handshake(func(c context.Context, _ Transport) (Transport, error) { return nil, fail(c) })
					case session.RDT:
						l.RDT = round2Reliable(func(c context.Context, _ Transport) (Transport, error) { return nil, fail(c) })
					case session.Tunnel:
						l.Tunnel = round2Tunneler(func(c context.Context, _ Transport) (Channels, error) { return nil, fail(c) })
					}
					return l, nil
				})
				var logs bytes.Buffer
				sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
				done := make(chan error, 1)
				go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, factory, nil, sink) }()
				<-entered
				cancel()
				err := <-done
				want := diagnostic.NewTerminal(layer, "timeout", session.ErrTransport).Phase()
				if p, r := diagnostic.Fields(err, ""); p != want || r != "timeout" {
					t.Errorf("joined native timeout lost: result=%s/%s want %s/timeout", p, r, want)
				}
				if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 2 {
					t.Errorf("independent joined timeout/capacity reports=%d want 2; logs=%s", n, &logs)
				}
				if f.allocated.Load() != f.stopped.Load() {
					t.Fatal("lease leaked")
				}
			})
		})
	}
}

func TestMultipleJoinedTypedNoticeCancellation(t *testing.T) {
	for _, layer := range []session.Layer{session.Rendezvous, session.DTLS, session.RDT, session.Tunnel} {
		t.Run(fmt.Sprint(layer), func(t *testing.T) {
			lost, wrongReport := 0, 0
			gotFields := map[string]int{}
			for range 24 {
				synctest.Test(t, func(t *testing.T) {
					f := &round2Driver{newFake(), make(chan func(core.Instant), 1)}
					f.notices[layer] = make(chan session.Event, 1)
					var logs bytes.Buffer
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
					done := make(chan error, 1)
					go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, f, nil, sink) }()
					synctest.Wait()
					f.work <- func(core.Instant) {
						f.notices[layer] <- session.Event{Kind: session.Failed, Err: errors.Join(diagnostic.NewTerminal(layer, "timeout", session.ErrTransport), diagnostic.NewTerminal(layer, "capacity", session.ErrTransport), context.Canceled)}
						cancel()
					}
					err := <-done
					want := diagnostic.NewTerminal(layer, "timeout", session.ErrTransport).Phase()
					p, r := diagnostic.Fields(err, "")
					gotFields[p+"/"+r]++
					if p != want || r != "timeout" {
						lost++
					}
					if strings.Count(logs.String(), `"level":"ERROR"`) != 2 || !strings.Contains(logs.String(), `"reason":"timeout"`) {
						wrongReport++
					}
					if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
						t.Fatal("ownership changed")
					}
				})
			}
			if lost != 0 || wrongReport != 0 {
				t.Errorf("joined outcome lost in %d/24 schedules; reports absent/misattributed in %d/24; outcomes=%v", lost, wrongReport, gotFields)
			}
		})
	}
}
