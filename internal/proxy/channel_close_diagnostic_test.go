package proxy

import (
	"bytes"
	"encoding/json/v2"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestChannelCloseDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		reason, want string
		peer         *p2ptunnel.CloseOutcome
	}{
		{"receive budget exceeded", "receive_budget", nil},
		{"peer close", "peer_close", &p2ptunnel.CloseOutcome{HasResult: true, Result: 1}},
		{"both directions ended", "ended", nil},
		{"local close", "local_close", nil},
		{"secret-sentinel", "transport_failed", nil},
	} {
		t.Run(tc.want, func(t *testing.T) {
			var b bytes.Buffer
			s := &Session{logger: slog.New(slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})), clients: make(map[uint64]*stream), in: make(chan session.Event, 1), done: make(chan struct{})}
			c := newStream(s, 1, 445)
			s.clients[1] = c
			x := &stack{f: &factory{s: s}, streams: map[uint64]*stream{1: c}}
			x.channels[1] = c
			x.tunnelAction(p2ptunnel.Action{Kind: p2ptunnel.CloseLocal, Channel: 1, Reason: tc.reason, PeerClose: tc.peer})
			var fields map[string]any
			if err := json.Unmarshal(b.Bytes(), &fields, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatalf("missing channel diagnostic: %q (%v)", b.String(), err)
			}
			if fields["msg"] != "channel_closed" || fields["layer"] != "tunnel" || fields["reason"] != tc.want || fields["channel"] != float64(1) {
				t.Fatalf("diagnostic: %v", fields)
			}
			if tc.peer != nil && (fields["peer_result"] != float64(1) || fields["peer_has_result"] != true || fields["local_end"] != false) {
				t.Fatalf("lost peer close context: %v", fields)
			}
			if strings.Contains(b.String(), "secret-sentinel") {
				t.Fatal("unfiltered reason leaked")
			}
		})
	}
}
