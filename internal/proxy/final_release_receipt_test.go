package proxy

import (
	"bytes"
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
)

func TestFinalEchoWithRealComposedSession(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		s, stacks, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{Logger: logger})
		c, err := s.DialPort(ctx, 445)
		if err != nil {
			t.Fatal(err)
		}
		x := <-stacks
		x.post(func(core.Instant) { x.failAt("rdt", "timeout"); cancel() })
		<-s.Done()
		c.Close()
		cleanupErr := s.Close()
		// Final diagnostics may receive the complete retained Close outcome.
		diagnostic.LogFinal(logger, "proxy failed", "session", cleanupErr, cleanupErr)
		if strings.Count(logs.String(), `"level":"ERROR"`) != 1 {
			t.Errorf("real stack primary echoed as a release error: %s", &logs)
		}
	})
}
