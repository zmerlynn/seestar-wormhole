package proxy

import (
	"bytes"
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"log/slog"
	"strings"
	"testing"
)

func TestCloudOwedReceiptEcho(t *testing.T) {
	raw := errors.Join(context.Canceled, cloud.ErrOwedRelease)
	terminal := cloudTerminal(raw)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	// Same projection the owner performs before observing/returning a terminal.
	diagnostic.Log(logger, "layer_failed", "session", diagnostic.RetainTerminal(terminal, cloudOutcome(raw)))
	diagnostic.LogFinal(logger, "proxy failed", "session", terminal, nil)
	if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 1 {
		t.Errorf("cloud owed outcome owner/final reports=%d want 1: %s", n, &logs)
	}
}
