package proxy

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-json-experiment/json"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// The API keeps CLOSE result 2, but the default daemon logger also needs the
// rejection cause: ordinary TCP consumers only see a closed connection.
func TestE2EReviewSetupRejectionLogged(t *testing.T) {
	var output bytes.Buffer
	s := &Session{logger: slog.New(slog.NewJSONHandler(&output, nil)), clients: make(map[uint64]*stream), in: make(chan session.Event, 1), done: make(chan struct{})}
	c := newStream(s, 1, 445)
	s.clients[1] = c
	x := &stack{f: &factory{s: s}, streams: map[uint64]*stream{1: c}}
	x.channels[1] = c
	x.tunnelAction(p2ptunnel.Action{Kind: p2ptunnel.CloseLocal, Channel: 1, Reason: "peer close", PeerClose: &p2ptunnel.CloseOutcome{HasResult: true, Result: 2}})
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, ErrSetupRejected) {
		t.Fatalf("API cause=%v, want setup rejection", err)
	}
	if output.Len() == 0 {
		t.Error("peer SETUP rejection retained by API but absent from default-level daemon log")
	}
}

// A bridge cannot return a device error to Serve, so it must report the
// classified cause before closing the TCP socket.
func TestE2EReviewBridgeFailureLogged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output bytes.Buffer
		a, client := net.Pipe()
		defer client.Close()
		b, device := net.Pipe()
		defer device.Close()
		reset := make(chan struct{})
		close(reset)
		bridge(a, &e2eResetConn{b, reset}, false, slog.New(slog.NewJSONHandler(&output, nil)))
		if output.Len() == 0 {
			t.Error("device reset closed TCP client without any default-level diagnostic")
		}
	})
}

func TestChannelFailureReportedOnce(t *testing.T) {
	for _, tc := range []struct {
		reason   string
		peer     *p2ptunnel.CloseOutcome
		abnormal bool
	}{
		{"peer close", &p2ptunnel.CloseOutcome{HasResult: true, Result: 2}, true},
		{"peer close", &p2ptunnel.CloseOutcome{HasResult: true, Result: 1}, true},
		{"peer close", &p2ptunnel.CloseOutcome{HasResult: true, Result: 255}, true},
		{"receive budget exceeded", nil, true},
		{"local write failed", nil, true},
		{"peer close", &p2ptunnel.CloseOutcome{HasResult: true, Result: 0}, false},
		{"peer close", &p2ptunnel.CloseOutcome{HasResult: true, Result: 1, LocalEnd: true}, false},
		{"both directions ended", nil, false},
		{"local close", nil, false},
	} {
		synctest.Test(t, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			s := &Session{logger: logger, clients: make(map[uint64]*stream), in: make(chan session.Event, 1), done: make(chan struct{})}
			c := newStream(s, 1, 445)
			s.clients[1] = c
			x := &stack{f: &factory{s: s}, streams: map[uint64]*stream{1: c}}
			x.channels[1] = c
			action := p2ptunnel.Action{Kind: p2ptunnel.CloseLocal, Channel: 1, Reason: tc.reason, PeerClose: tc.peer}
			x.tunnelAction(action)
			x.tunnelAction(action)
			a, client := net.Pipe()
			defer client.Close()
			bridge(a, c, false, logger)
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			if !tc.abnormal {
				if output.Len() != 0 {
					t.Fatalf("ordinary close logged: %s", &output)
				}
				return
			}
			if len(lines) != 1 {
				t.Fatalf("failure records=%d, want 1: %s", len(lines), &output)
			}
			var fields map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &fields, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatal(err)
			}
			if fields["port"] != float64(445) || fields["channel"] != float64(1) || fields["level"] != "WARN" {
				t.Fatalf("missing channel context: %v", fields)
			}
			if tc.peer != nil && fields["peer_result"] != float64(tc.peer.Result) {
				t.Fatalf("lost peer result: %v", fields)
			}
			if tc.peer != nil && tc.peer.Result == 2 && fields["cause"] != "setup_rejected" {
				t.Fatalf("lost setup cause: %v", fields)
			}
			if errors.Is(c.terminal, io.EOF) {
				t.Fatal("abnormal close became EOF")
			}
		})
	}
}

func TestCanceledAdmissionLoggedAtDefaultLevel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		a, peer := net.Pipe()
		defer peer.Close()
		l := &e2eSingleListener{conn: a, done: make(chan struct{})}
		d := &e2ePendingDial{started: make(chan struct{}), canceled: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			done <- Serve(ctx, d, []Forward{{Listener: l, Port: 445}}, 1, slog.New(slog.NewJSONHandler(&logs, nil)))
		}()
		<-d.started
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(logs.String(), `"msg":"client_canceled"`) || !strings.Contains(logs.String(), `"level":"INFO"`) || !strings.Contains(logs.String(), `"reason":"canceled"`) {
			t.Fatalf("missing INFO admission cancellation: %s", &logs)
		}
	})
}

func TestOrdinaryChannelCloseRetainsEOFIdentity(t *testing.T) {
	for _, action := range []p2ptunnel.Action{
		{Kind: p2ptunnel.CloseLocal, Channel: 1, Reason: "both directions ended"},
		{Kind: p2ptunnel.CloseLocal, Channel: 1, Reason: "peer close", PeerClose: &p2ptunnel.CloseOutcome{HasResult: true, Result: 0}},
		{Kind: p2ptunnel.CloseLocal, Channel: 1, Reason: "peer close", PeerClose: &p2ptunnel.CloseOutcome{HasResult: true, Result: 1, LocalEnd: true}},
	} {
		s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 1), done: make(chan struct{})}
		c := newStream(s, 1, 445)
		s.clients[1] = c
		x := &stack{f: &factory{s: s}, streams: map[uint64]*stream{1: c}}
		x.channels[1] = c
		x.tunnelAction(action)
		if c.terminal != io.EOF {
			t.Errorf("ordinary close lost EOF identity: %T", c.terminal)
		}
		if _, err := io.ReadAll(c); err != nil {
			t.Errorf("ordinary close failed io.ReadAll: %v", err)
		}
	}
}
