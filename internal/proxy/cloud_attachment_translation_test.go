package proxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func TestCloudAttachedTranslationEcho(t *testing.T) {
	raw := diagnostic.Wrap("cloud", "canceled", errors.Join(context.Canceled, cloud.ErrOwedRelease))
	first := cloudTerminal(raw)
	second := cloudTerminal(raw)
	var logs bytes.Buffer
	l := slog.New(slog.NewJSONHandler(&logs, nil))
	diagnostic.Log(l, "owner", "session", first)
	diagnostic.LogFinal(l, "final", "session", second, nil)
	if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 1 {
		t.Errorf("one cloud attachment translated twice gives %d reports, want 1: %s", n, &logs)
	}
}
