package proxy

import (
	"bytes"
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
)

func TestTunnelTeardownRetainsCause(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var logs bytes.Buffer
		s, stacks, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
		c, err := s.DialPort(ctx, 445)
		if err != nil {
			t.Fatal(err)
		}
		x := <-stacks
		sibling, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		// A DATA header claiming 65535 bytes violates the maintained tunnel bound.
		x.post(func(now core.Instant) {
			x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{2, 1, 255, 255}}}, now)
		})
		_, err = c.Read(make([]byte, 1))
		var terminal *diagnostic.Terminal
		if !errors.As(err, &terminal) {
			t.Errorf("client lost typed tunnel terminal cause: %T %v", err, err)
		} else if p, r := diagnostic.Fields(err, ""); p != "tunnel" || r != "invalid_frame" {
			t.Errorf("client cause=%s/%s, want tunnel/invalid_frame", p, r)
		}
		_, siblingErr := sibling.Read(make([]byte, 1))
		// Reads can resume between CloseLocal and SessionClosed. Join the
		// owner batch before checking the stack's terminal publication.
		synctest.Wait()
		var siblingTerminal *diagnostic.Terminal
		if !errors.As(siblingErr, &siblingTerminal) || siblingTerminal != terminal || terminal != x.terminal.Load() {
			t.Errorf("clients and stack did not share one terminal: client=%v sibling=%v stack=%v", terminal, siblingTerminal, x.terminal.Load())
		}
		c.Close()
		sibling.Close()
		synctest.Wait()
		s.Close()
		if strings.Count(logs.String(), `"level":"ERROR"`) != 1 {
			t.Errorf("want exactly one failure report: %s", &logs)
		}
	})
}

func TestCancellationKeepsOrdinaryTunnelStopQuiet(t *testing.T) {
	var logs bytes.Buffer
	s := &Session{logger: slog.New(slog.NewJSONHandler(&logs, nil)), clients: make(map[uint64]*stream), in: make(chan session.Event, 2), done: make(chan struct{})}
	c := newStream(s, 1, 445)
	s.clients[1] = c
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x := &stack{ctx: ctx, cancel: cancel, f: &factory{s: s}, streams: map[uint64]*stream{1: c}}
	for i := range x.handles {
		x.handles[i] = &layerHandle{notices: make(chan session.Event, 1)}
	}
	x.channels[1] = c
	x.tunnelAction(p2ptunnel.Action{Kind: p2ptunnel.CloseLocal, Channel: 1, Reason: "session stopped"})
	x.tunnelAction(p2ptunnel.Action{Kind: p2ptunnel.SessionClosed, Reason: "session stopped"})
	if strings.Contains(logs.String(), `"level":"WARN"`) || strings.Contains(logs.String(), `"level":"ERROR"`) {
		t.Errorf("operator stop logged abnormal failure: %s", &logs)
	}
	if _, reason := diagnostic.Fields(x.terminalCause(session.ErrTransport), ""); reason != "canceled" {
		t.Errorf("stop reason=%s", reason)
	}
}
