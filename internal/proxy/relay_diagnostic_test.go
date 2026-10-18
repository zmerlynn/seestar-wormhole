package proxy

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestRelayProgressEdgeDiagnostics(t *testing.T) {
	var b bytes.Buffer
	x := &stack{f: &factory{s: &Session{logger: slog.New(slog.NewJSONHandler(&b, nil))}}, cancel: func() {}}
	for i := range x.handles {
		x.handles[i] = &layerHandle{notices: make(chan session.Event, 1)}
	}
	p := iotc.RelayProgress{Phase: iotc.RelayInfoOutstanding, Requests: 3, PendingResults: 1, ReadyResults: 11, RefusedResults: 2, ServerRefusedResults: 2, RefusedPaths: 1, InfoRequests: 118, InfoOutstanding: 1}
	a := iotc.Action{Kind: iotc.RelayProgressChanged, Progress: p, Reason: "secret-sentinel", Domain: "secret-domain", Data: []byte("secret-payload"), Link: iotc.Link{Address: netip.MustParseAddrPort("203.0.113.1:10001"), ID: 42}, Attempt: 123, Token: 456}
	x.iotcAction(a)
	a.Kind, a.Failures = iotc.Failed, iotc.ServerRefused|iotc.ConnectionTimeout
	x.iotcAction(a)
	x.iotcAction(a) // A terminal callback cannot duplicate the summary.
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("log events = %d, want one phase change and terminal summary: %s", len(lines), b.String())
	}
	for i, line := range lines {
		var fields map[string]any
		if err := json.Unmarshal([]byte(line), &fields, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		wantMsg := "relay_progress"
		if i == 1 {
			wantMsg = "relay_failure_summary"
		}
		if fields["msg"] != wantMsg || fields["phase"] != "rendezvous" || fields["relay_phase"] != "info_outstanding" || fields["relay_info_requests"] != float64(118) || fields["relay_info_outstanding"] != float64(1) || fields["relay_refused_paths"] != float64(1) || fields["relay_ready_results"] != float64(11) || fields["relay_server_refused_results"] != float64(2) {
			t.Fatalf("event %d = %v, want structured progress", i, fields)
		}
		allowed := " time level msg phase reason relay_phase relay_requests relay_pending_results relay_ready_results relay_refused_results relay_server_refused_results relay_refused_paths relay_info_requests relay_info_outstanding relay_info_received relay_selected "
		for key := range fields {
			if !strings.Contains(allowed, " "+key+" ") {
				t.Errorf("unexpected log field %q", key)
			}
		}
		if i == 1 && fields["reason"] != "relay accepted; relay info never arrived after 118 requests; server refused; connection timeout" {
			t.Fatalf("terminal reason = %v", fields["reason"])
		}
	}
	for _, secret := range []string{"secret-", "203.0.113", "10001"} {
		if strings.Contains(b.String(), secret) {
			t.Errorf("sensitive action field leaked: %s", secret)
		}
	}
	if len(x.effects) != 0 {
		t.Fatalf("diagnostic actions reached I/O executor: %d", len(x.effects))
	}
}

func TestRelaySelectedTerminalSummary(t *testing.T) {
	var b bytes.Buffer
	x := &stack{f: &factory{s: &Session{logger: slog.New(slog.NewJSONHandler(&b, nil))}}}
	p := iotc.RelayProgress{Phase: iotc.RelaySelected, Requests: 1, ReadyResults: 1, InfoRequests: 1, InfoReceived: 1, Selected: true}
	x.iotcAction(iotc.Action{Kind: iotc.Ready, Progress: p})
	var fields map[string]any
	if err := json.Unmarshal(b.Bytes(), &fields, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatalf("missing terminal success summary: %s (%v)", b.String(), err)
	}
	if fields["msg"] != "relay_summary" || fields["relay_phase"] != "selected" || fields["relay_selected"] != true || fields["relay_info_received"] != float64(1) {
		t.Fatalf("success summary = %v", fields)
	}
}
