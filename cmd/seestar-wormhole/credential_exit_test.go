package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// A refused login's final record names the account it tried, never the
// password.
func TestUnauthorizedRecordNamesAccount(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "test@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "PRIVATE_PASSWORD")
	_, unauthorized := cloud.ParseLogin(401, nil, false)
	var logs bytes.Buffer
	err := runWithListener(t.Context(), []string{"proxy", "--log-level=error"}, io.Discard, &logs, func(context.Context, proxy.Config) (commandSession, error) {
		return nil, diagnostic.AtLayer(unauthorized, session.Cloud, session.ErrUncertain)
	}, func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") })
	if code := commandExitCode(options{command: commandProxy}, err); code != 3 {
		t.Fatalf("exit=%d: %v", code, err)
	}
	found := false
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		var record map[string]any
		if json.Unmarshal(line, &record) != nil || record["msg"] != "proxy failed" {
			continue
		}
		found = true
		if record["reason"] != "unauthorized" || record["account"] != "test@example.invalid" {
			t.Errorf("final record: %v", record)
		}
	}
	if !found || strings.Contains(logs.String(), "PRIVATE_PASSWORD") {
		t.Fatalf("logs: %s", &logs)
	}
}

func TestCredentialExitCode(t *testing.T) {
	_, unauthorized := cloud.ParseLogin(401, nil, false)
	_, forbidden := cloud.ParseLogin(403, nil, false)
	_, unavailable := cloud.ParseLogin(503, nil, false)
	for _, tc := range []struct {
		name, reason string
		err          error
		code         int
	}{
		{"login unauthorized", "unauthorized", unauthorized, 3},
		{"login forbidden", "unauthorized", forbidden, 3},
		{"denial with body timeout", "unauthorized", cloud.JoinOutcomes(unauthorized, context.DeadlineExceeded), 3},
		{"reauthentication", "reauthentication_required", cloud.ErrReauthentication, 3},
		{"server unavailable", "rejected", unavailable, 1},
		{"transport", "transport_failed", cloud.ErrTransport, 1},
		{"busy", "busy", cloud.ErrBusy, 1},
		{"uncertain account", "operation_failed", cloud.ErrAccountUncertain, 1},
		{"uncertain ownership", "ownership_uncertain", session.ErrUncertain, 1},
		{"generic rejection", "rejected", cloud.ErrRejected, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := diagnostic.AtLayer(tc.err, session.Cloud, session.ErrUncertain)
			_, reason := diagnostic.Fields(err, "")
			if reason != tc.reason {
				t.Errorf("reason=%s, want %s", reason, tc.reason)
			}
			if code := commandExitCode(options{command: commandProxy}, err); code != tc.code {
				t.Errorf("exit=%d, want %d", code, tc.code)
			}
		})
	}
	if code := commandExitCode(options{command: commandProxy}, diagnostic.NewTerminal(session.Tunnel, "login_rejected", session.ErrTransport)); code != 1 {
		t.Errorf("tunnel login exit=%d, want 1", code)
	}

	var o options
	if proxy, _, err := newCommand(&o, io.Discard).Find([]string{"proxy"}); err != nil || !strings.Contains(proxy.Long, "3 on terminal cloud credential or authorization rejection") {
		t.Fatalf("proxy help must document exit 3: %v", err)
	}

	unit, err := os.ReadFile("../../docs/always-on.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), fmt.Sprintf("RestartPreventExitStatus=%d\n", 3)) {
		t.Fatal("documented service must prevent restart after credential rejection")
	}
}
