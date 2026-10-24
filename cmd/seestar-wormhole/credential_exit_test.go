package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

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

	unit, err := os.ReadFile("../../docs/always-on.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), fmt.Sprintf("RestartPreventExitStatus=%d\n", 3)) {
		t.Fatal("documented service must prevent restart after credential rejection")
	}
}
