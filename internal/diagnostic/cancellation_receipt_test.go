package diagnostic

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"log/slog"
	"strings"
	"testing"
)

func TestAbandonedCancellationReceipt(t *testing.T) {
	terminal := NewTerminal(session.Cloud, "canceled", session.ErrReleaseAbandoned)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	LogFinal(logger, "proxy failed", "session", RetainTerminal(terminal, session.ErrReleaseAbandoned), nil)
	LogFinal(logger, "proxy failed", "session", terminal, nil)
	if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 1 {
		t.Errorf("one received owed-release outcome reports=%d want 1: %s", n, &logs)
	}
}
