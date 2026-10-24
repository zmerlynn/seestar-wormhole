package sessionedge

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"testing/synctest"

	"encoding/json/jsontext"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestConstructorFailureAttributionMatrix(t *testing.T) {
	rows := 0
	for layer := session.Cloud; layer < session.LayerCount; layer++ {
		for origin := session.Cloud; origin <= diagnostic.StackLayer; origin++ {
			for exit := 0; exit < 3; exit++ {
				for receiptOrder := 0; receiptOrder < 2; receiptOrder++ {
					rows++
					t.Run(fmt.Sprintf("phase%d_origin%d_exit%d_early%d", layer, origin, exit, receiptOrder), func(t *testing.T) {
						synctest.Test(t, func(t *testing.T) {
							f := newFake()
							entered := make(chan struct{})
							ctx, cancel := context.WithCancel(context.Background())
							defer cancel()
							in := make(chan session.Event, 1)
							var logs bytes.Buffer
							l := slog.New(slog.NewJSONHandler(&logs, nil))
							a := diagnostic.NewTerminal(origin, "timeout", session.ErrTransport)
							b := diagnostic.NewTerminal(origin, "timeout", session.ErrTransport)
							if receiptOrder == 1 {
								diagnostic.Log(l, "early", "session", b)
							}
							fail := func(c context.Context) error {
								close(entered)
								<-c.Done()
								return fmt.Errorf("PRIVATE_COORDS PRIVATE_TIME SECRET_UID PRIVATE_BYTES: %w", errors.Join(context.Canceled, a, b))
							}
							factory := factoryFunc(func(id uint64, budget session.Budgets) (Layers, error) {
								layers, _ := f.New(id, budget)
								switch layer {
								case session.Cloud:
									layers.Cloud = allocateFunc(func(c context.Context) (Lease, error) { f.allocated.Add(1); return f, fail(c) })
								case session.Rendezvous:
									layers.Rendezvous = round2Rendezvous(func(c context.Context, _ Lease) (Transport, error) { return nil, fail(c) })
								case session.DTLS:
									layers.DTLS = round2Handshake(func(c context.Context, _ Transport) (Transport, error) { return nil, fail(c) })
								case session.RDT:
									layers.RDT = round2Reliable(func(c context.Context, _ Transport) (Transport, error) { return nil, fail(c) })
								case session.Tunnel:
									layers.Tunnel = round2Tunneler(func(c context.Context, _ Transport) (Channels, error) { return nil, fail(c) })
								}
								return layers, nil
							})
							sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: l}
							done := make(chan error, 1)
							go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, factory, in, sink) }()
							<-entered
							switch exit {
							case 0:
								cancel()
							case 1:
								in <- session.Event{Kind: session.Shutdown}
							case 2:
								close(in)
							}
							err := <-done
							if p, r := diagnostic.Fields(err, ""); p != a.Phase() || r != "timeout" {
								t.Errorf("primary=%s/%s want %s/timeout", p, r, a.Phase())
							}
							before := logs.Len()
							diagnostic.LogFinal(l, "final", "session", err, nil)
							diagnostic.LogFinal(l, "echo", "session", errors.Join(a, b), nil)
							if logs.Len() != before {
								t.Errorf("new report on final echoes: %s", &logs)
							}
							d := jsontext.NewDecoder(&logs)
							n := 0
							for d.PeekKind() != 0 {
								var row struct{ Phase, Reason string }
								if err := json.UnmarshalDecode(d, &row, json.MatchCaseInsensitiveNames(true)); err != nil {
									t.Fatal(err)
								}
								n++
								if row.Phase != a.Phase() || row.Reason != "timeout" {
									t.Errorf("wrong attribution=%+v", row)
								}
							}
							if n != 2 {
								t.Errorf("reports=%d want 2", n)
							}
							if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
								t.Errorf("allocations/stops=%d/%d want 1/1", f.allocated.Load(), f.stopped.Load())
							}
						})
					})
				}
			}
		}
	}
	t.Logf("rows=%d", rows)
}
