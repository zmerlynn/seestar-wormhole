package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestFinalJoinedTerminalCauses(t *testing.T) {
	for _, cancelFirst := range []bool{false, true} {
		cancel := NewTerminal(session.Cloud, "canceled", session.ErrTransport)
		received := NewTerminal(session.RDT, "timeout", session.ErrTransport)
		outcome := errors.Join(received, cancel)
		if cancelFirst {
			outcome = errors.Join(cancel, received)
		}
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		LogFinal(logger, "proxy failed", "session", outcome, nil)
		LogFinal(logger, "proxy failed", "session", received, nil)
		if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 1 || !strings.Contains(logs.String(), `"phase":"rdt"`) || !strings.Contains(logs.String(), `"reason":"timeout"`) {
			t.Errorf("cancelFirst=%v report/echo=%s want one rdt/timeout report", cancelFirst, &logs)
		}
	}
}

func TestJoinedCancellationSelectsAttachedCause(t *testing.T) {
	canceled := NewTerminal(session.Cloud, "canceled", session.ErrTransport)
	outcome := errors.Join(canceled, Wrap("rdt", "timeout", session.ErrTransport), context.Canceled)
	if phase, reason := Fields(outcome, "session"); phase != "rdt" || reason != "timeout" {
		t.Fatalf("selected=%s/%s, want rdt/timeout", phase, reason)
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	LogFinal(logger, "proxy failed", "session", outcome, nil)
	LogFinal(logger, "proxy failed", "session", canceled, nil)
	if strings.Count(logs.String(), `"level":"ERROR"`) != 1 || !strings.Contains(logs.String(), `"phase":"rdt"`) || !strings.Contains(logs.String(), `"reason":"timeout"`) {
		t.Fatalf("attached cause/cancellation reports=%s, want one rdt/timeout", &logs)
	}
}

func TestCancellationTerminalBlockingPolicyKeepsReceipt(t *testing.T) {
	for _, policy := range []error{session.ErrStop, session.ErrUncertain, session.ErrBusy} {
		terminal := NewTerminal(session.Cloud, "canceled", policy)
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		LogFinal(logger, "proxy failed", "session", RetainTerminal(terminal, policy), nil)
		LogFinal(logger, "proxy failed", "session", terminal, nil)
		if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 1 {
			t.Errorf("policy=%v: reports=%d, want 1: %s", policy, n, &logs)
		}
	}
}
