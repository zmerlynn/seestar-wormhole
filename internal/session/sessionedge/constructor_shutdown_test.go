package sessionedge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
)

func TestShutdownDuringFailedConstructorRetainsOutcome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		f := newFake()
		entered := make(chan struct{})
		cause := diagnostic.NewTerminal(session.Cloud, "device_offline", session.ErrTransport)
		factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
			l, _ := f.New(a, b)
			l.Cloud = allocateFunc(func(ctx context.Context) (Lease, error) { close(entered); <-ctx.Done(); return nil, cause })
			return l, nil
		})
		sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
		in := make(chan session.Event, 1)
		done := make(chan error, 1)
		go func() { done <- Run(context.Background(), session.DefaultConfig(), rng{}, factory, in, sink) }()
		<-entered
		in <- session.Event{Kind: session.Shutdown}
		err := <-done
		var terminal *diagnostic.Terminal
		if !errors.As(err, &terminal) {
			t.Errorf("explicit shutdown discarded current constructor failure: result=%v failures=%d logs=%s", err, len(sink.failed), &logs)
		}
		if p, r := diagnostic.Fields(err, ""); p != "cloud" || r != "device_offline" {
			t.Errorf("shutdown outcome=%s/%s, want cloud/device_offline", p, r)
		}
		if len(sink.failed) != 1 || strings.Count(logs.String(), `"level":"ERROR"`) != 1 {
			t.Errorf("shutdown report count: failures=%d logs=%s", len(sink.failed), &logs)
		}
		if f.allocated.Load() != 0 || f.stopped.Load() != 0 {
			t.Fatal("failed constructor changed lease ownership")
		}
	})
}

func TestConstructorCancellationEveryPhase(t *testing.T) {
	for layer := session.Cloud; layer < session.LayerCount; layer++ {
		t.Run(fmt.Sprint(layer), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFake()
				f.blockAt = int(layer)
				f.partial = true
				// Each constructor observes a typed timeout before yielding to parent exit.
				cause := diagnostic.NewTerminal(layer, "timeout", session.ErrTransport)
				entered := make(chan struct{})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
					l, _ := f.New(a, b)
					switch layer {
					case session.Cloud:
						l.Cloud = allocateFunc(func(c context.Context) (Lease, error) { close(entered); <-c.Done(); return nil, cause })
					case session.Rendezvous:
						l.Rendezvous = round2Rendezvous(func(c context.Context, _ Lease) (Transport, error) { close(entered); <-c.Done(); return nil, cause })
					case session.DTLS:
						l.DTLS = round2Handshake(func(c context.Context, _ Transport) (Transport, error) { close(entered); <-c.Done(); return nil, cause })
					case session.RDT:
						l.RDT = round2Reliable(func(c context.Context, _ Transport) (Transport, error) { close(entered); <-c.Done(); return nil, cause })
					case session.Tunnel:
						l.Tunnel = round2Tunneler(func(c context.Context, _ Transport) (Channels, error) { close(entered); <-c.Done(); return nil, cause })
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
				if p, r := diagnostic.Fields(err, ""); p != cause.Phase() || r != "timeout" {
					t.Errorf("phase=%d owner terminal=%s/%s", layer, p, r)
				}
				if len(sink.failed) != 1 || strings.Count(logs.String(), `"level":"ERROR"`) != 1 {
					t.Errorf("phase=%d failures=%d logs=%s", layer, len(sink.failed), &logs)
				}
				if f.allocated.Load() != f.stopped.Load() {
					t.Fatal("lease leaked")
				}
			})
		})
	}
}

type round2Rendezvous func(context.Context, Lease) (Transport, error)

func (f round2Rendezvous) Dial(c context.Context, l Lease) (Transport, error) { return f(c, l) }

type round2Handshake func(context.Context, Transport) (Transport, error)

func (f round2Handshake) Handshake(c context.Context, l Transport) (Transport, error) { return f(c, l) }

type round2Reliable func(context.Context, Transport) (Transport, error)

func (f round2Reliable) Create(c context.Context, l Transport) (Transport, error) { return f(c, l) }

type round2Tunneler func(context.Context, Transport) (Channels, error)

func (f round2Tunneler) Start(c context.Context, l Transport) (Channels, error) { return f(c, l) }

func TestSuccessfulShutdownRemainsQuiet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		f := newFake()
		sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
		in := make(chan session.Event, 1)
		done := make(chan error, 1)
		go func() { done <- Run(context.Background(), session.DefaultConfig(), rng{}, f, in, sink) }()
		synctest.Wait()
		in <- session.Event{Kind: session.Shutdown}
		if err := <-done; err != nil {
			t.Errorf("successful Shutdown=%v, want nil", err)
		}
		if len(sink.failed) != 0 || strings.Contains(logs.String(), `"level":"ERROR"`) {
			t.Errorf("successful Shutdown reported a failure: %s", &logs)
		}
		if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
			t.Fatal("ownership changed")
		}
	})
}
