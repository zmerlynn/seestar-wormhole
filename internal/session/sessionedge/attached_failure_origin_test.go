package sessionedge

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestAttachedCauseRetainsOrigin(t *testing.T) {
	for layer := session.Cloud; layer < session.LayerCount; layer++ {
		phase := diagnostic.NewTerminal(layer, "timeout", session.ErrTransport).Phase()
		cancelLayer := (layer + 1) % session.LayerCount
		raw := errors.Join(diagnostic.NewTerminal(cancelLayer, "canceled", session.ErrTransport), diagnostic.Wrap(phase, "timeout", session.ErrTimeout), context.Canceled)
		got := constructorFailure(raw, layer)
		if p, r := diagnostic.Fields(got, ""); p != phase || r != "timeout" {
			t.Errorf("constructor layer=%s retained=%s/%s want %s/timeout", phase, p, r, phase)
		}
		var logs bytes.Buffer
		diagnostic.LogFinal(slog.New(slog.NewJSONHandler(&logs, nil)), "proxy failed", "session", got, nil)
		if !strings.Contains(logs.String(), `"phase":"`+phase+`"`) {
			t.Errorf("wrong origin: %s", &logs)
		}
	}
}

func TestNativeSiblingUsesActualLayer(t *testing.T) {
	for layer := session.Cloud; layer < session.LayerCount; layer++ {
		cancellation := diagnostic.NewTerminal((layer+1)%session.LayerCount, "canceled", session.ErrTransport)
		err := constructorFailure(errors.Join(cancellation, session.ErrTimeout, context.Canceled), layer)
		want := diagnostic.NewTerminal(layer, "timeout", session.ErrTransport).Phase()
		if p, r := diagnostic.Fields(err, ""); p != want || r != "timeout" {
			t.Fatalf("layer=%v native cause=%s/%s want %s/timeout", layer, p, r, want)
		}
	}
}
