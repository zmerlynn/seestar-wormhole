package main

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
)

func TestFinalTraceSummary(t *testing.T) {
	for _, command := range []string{"proxy"} {
		t.Run(command, func(t *testing.T) {
			var logs, out bytes.Buffer
			factory := func(_ context.Context, cfg proxy.Config) (commandSession, error) {
				// Stage one unreported late-record loss. The command's actual deferred
				// Close must report it exactly once and retain its failure receipt.
				cfg.Trace.Close()
				cfg.Trace.SetLogger(nil)
				cfg.Trace.Record("dtls", 1, 0, tracerecord.EdgeEvent{}, []tracerecord.EdgeEvent{})
				cfg.Trace.SetLogger(cfg.Logger)
				done := make(chan struct{})
				close(done)
				return &finalFailureSession{idleCommandSession: idleCommandSession{done}}, nil
			}
			args := []string{command, "--trace=" + filepath.Join(t.TempDir(), "trace"), "--log-format=json"}
			err := runWithListener(t.Context(), args, &out, &logs, factory,
				func(string, string) (net.Listener, error) {
					return &finalFailureListener{done: make(chan struct{})}, nil
				})
			if err == nil {
				t.Fatal("missing final loss receipt")
			}
			if n := strings.Count(logs.String(), `"rule":"trace_loss"`); n != 1 {
				t.Fatalf("want one final trace summary, got %d: %s", n, &logs)
			}
		})
	}
}
