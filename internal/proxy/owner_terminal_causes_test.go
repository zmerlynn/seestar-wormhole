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

func TestOwnerCancellationRetainsReadyFailure(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	lost := 0
	for range 24 {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var logs bytes.Buffer
			s, stacks, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
			c, err := s.DialPort(ctx, 445)
			if err != nil {
				t.Fatal(err)
			}
			x := <-stacks
			x.post(func(core.Instant) { x.failAt("rdt", "timeout"); cancel() })
			<-s.Done()
			_, err = c.Read(make([]byte, 1))
			if p, r := diagnostic.Fields(err, ""); p != "rdt" || r != "timeout" {
				t.Errorf("stream lost cause=%s/%s", p, r)
			}
			c.Close()
			closeErr := s.Close()
			if p, r := diagnostic.Fields(closeErr, ""); p != "rdt" || r != "timeout" {
				t.Errorf("owner result lost cause=%s/%s", p, r)
			}
			if strings.Count(logs.String(), `"level":"ERROR"`) != 1 {
				lost++
			}
		})
	}
	if lost != 0 {
		t.Errorf("owner cancellation erased default-visible ready RDT failure in %d/24 schedules", lost)
	}
}
