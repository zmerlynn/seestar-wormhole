package proxy

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/edge"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
)

func TestStackFailureHasSingleOrigin(t *testing.T) {
	for _, phase := range []string{"rendezvous", "dtls", "rdt", "tunnel", "stack"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			x := &stack{ctx: ctx, cancel: cancel, f: &factory{s: &Session{}}}
			for i := range x.handles {
				x.handles[i] = &layerHandle{notices: make(chan session.Event, 1)}
			}
			for i := range x.phases {
				x.phases[i] = make(chan error, 1)
			}
			x.failAt(phase, "timeout")
			x.failAt("dtls", "write_failed")
			count := 0
			for _, h := range x.handles {
				for len(h.notices) > 0 {
					e := <-h.notices
					count++
					gotPhase, reason := diagnostic.Fields(e.Err, "unknown")
					if gotPhase != phase || reason != "timeout" {
						t.Errorf("terminal cause=%s/%s, want %s/timeout", gotPhase, reason, phase)
					}
				}
			}
			if count != 1 {
				t.Errorf("terminal notifications=%d, want 1", count)
			}
			for n := range x.phases {
				err := x.wait(context.Background(), n)
				gotPhase, reason := diagnostic.Fields(err, "unknown")
				if gotPhase != phase || reason != "timeout" {
					t.Errorf("phase waiter=%s/%s, want %s/timeout", gotPhase, reason, phase)
				}
			}
		})
	}
}

func TestReadyStackFailureReportingEveryPhase(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	for _, phase := range []string{"rendezvous", "dtls", "rdt", "tunnel"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs bytes.Buffer
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s, stacks, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
				c, err := s.DialPort(ctx, 445)
				if err != nil {
					t.Fatal(err)
				}
				x := <-stacks
				x.post(func(core.Instant) {
					if phase == "rendezvous" {
						x.iotcAction(iotc.Action{Kind: iotc.Failed, Reason: "liveness timeout"})
					} else {
						x.failAt(phase, "timeout")
					}
				})
				_, err = c.Read(make([]byte, 1))
				if err == nil {
					t.Fatal("stack failure hidden")
				}
				c.Close()
				synctest.Wait()
				s.Close()
				reports := 0
				foundTransition := false
				for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
					var record struct{ Level, Msg, Phase, Layer, Reason string }
					if err := json.Unmarshal([]byte(line), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
						t.Fatal(err)
					}
					if record.Level == "ERROR" {
						reports++
						want := "timeout"
						if phase == "rendezvous" {
							want = "liveness_timeout"
						}
						if record.Phase != phase || record.Reason != want {
							t.Errorf("report=%+v, want %s/%s", record, phase, want)
						}
					}
					if record.Msg == "session_state" && record.Layer == phase {
						foundTransition = true
					}
				}
				if reports != 1 || !foundTransition {
					t.Fatalf("error reports=%d, attributed transition=%v: %s", reports, foundTransition, &logs)
				}
				for _, secret := range []string{"token-sentinel", "source-sentinel", "sim-user", "sim-pass", "EXAMPLEUID", "sim-license"} {
					if strings.Contains(logs.String(), secret) {
						t.Fatal("secret escaped")
					}
				}
			})
		})
	}
}

func TestConstructorAndTerminalShareReport(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	err := diagnostic.NewTerminal(session.RDT, "timeout", session.ErrTransport)
	finish := diagnostic.Operation(diagnostic.Lifecycle(logger), "rdt")
	finish(err)
	diagnostic.Log(logger, "layer_failed", "session", err)
	diagnostic.Log(logger, "layer_failed", "session", diagnostic.RetainTerminal(err, session.ErrTransport))
	diagnostic.Log(logger, "proxy failed", "session", err)
	if strings.Count(logs.String(), `"level":"ERROR"`) != 1 {
		t.Fatalf("duplicate report: %s", &logs)
	}
}

func TestStackFailureOverridesQueuedReadiness(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x := &stack{ctx: ctx, cancel: cancel, f: &factory{s: &Session{}}}
	for i := range x.handles {
		x.handles[i] = &layerHandle{notices: make(chan session.Event, 1)}
	}
	for i := range x.phases {
		x.phases[i] = make(chan error, 1)
		x.phases[i] <- nil
	}
	x.failAt("rdt", "timeout")
	for i := range x.phases {
		if err := x.wait(context.Background(), i); err == nil {
			t.Fatal("terminal stack admitted readiness")
		}
	}
}

func TestSessionRejectionRetainsTypedCause(t *testing.T) {
	s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 1), done: make(chan struct{})}
	c := newStream(s, 1, 445)
	s.clients[1] = c
	cause := diagnostic.NewTerminal(session.RDT, "timeout", session.ErrTransport)
	s.Observe(session.Event{Kind: session.Failed, Layer: session.RDT, Err: cause}, session.Snapshot{State: session.Dialing}, session.Snapshot{State: session.Releasing}, 0)
	s.Try(session.Action{Kind: session.Reject, Client: 1, Err: session.ErrTransport})
	err := <-c.result
	phase, reason := diagnostic.Fields(err, "")
	if phase != "rdt" || reason != "timeout" {
		t.Fatalf("rejected admission lost stack cause: %s/%s", phase, reason)
	}
}

func TestStackCancellationDoesNotEraseOrigin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x := &stack{ctx: ctx, cancel: cancel, f: &factory{s: &Session{}}}
	for i := range x.handles {
		x.handles[i] = &layerHandle{notices: make(chan session.Event, 1)}
	}
	for i := range x.phases {
		x.phases[i] = make(chan error, 1)
	}
	x.failAt("rdt", "timeout")
	caller, stop := context.WithCancel(context.Background())
	stop()
	for n := range x.phases {
		for range 3 {
			err := x.wait(caller, n)
			phase, reason := diagnostic.Fields(err, "")
			if phase != "rdt" || reason != "timeout" {
				t.Errorf("cancellation erased origin: %s/%s", phase, reason)
			}
		}
	}
	packets, err := edge.NewDTLSPackets(1388)
	if err != nil {
		t.Fatal(err)
	}
	defer packets.Close()
	packets.Close()
	x.packets = packets
	_, err = x.Handshake(caller, nil)
	phase, reason := diagnostic.Fields(err, "")
	if phase != "rdt" || reason != "timeout" {
		t.Errorf("constructor erased stack cause: %s/%s", phase, reason)
	}
}
