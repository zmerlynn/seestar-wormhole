package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
)

func TestFinalKeylogSummary(t *testing.T) {
	for _, mode := range []string{"close", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs progressBuffer
				sink := &queueLossSink{mode: mode}
				if mode == "timeout" {
					sink.entered, sink.release = make(chan struct{}, 1), make(chan struct{})
				}
				s := newStartupLogging(options{format: "json", level: "info"}, &logs)
				s.openKeys("synthetic", func(_ string, logger *slog.Logger) (*keylog.Writer, error) {
					return keylog.New(sink, logger), nil
				})
				released := false
				defer func() {
					if sink.release != nil && !released {
						close(sink.release)
						s.keys.Close()
					}
				}()
				s.acceptKeys()
				s.output.resolve(true)
				if sink.entered != nil {
					s.keys.Write([]byte("key\n"))
					<-sink.entered
				}
				if err := s.finish(nil); err == nil {
					t.Fatal("missing final loss receipt")
				}
				synctest.Wait()
				if n := strings.Count(logs.String(), `"rule":"keylog_loss"`); n != 1 {
					t.Fatalf("want one final keylog summary, got %d: %s", n, logs.String())
				}
				if sink.release != nil {
					close(sink.release)
					released = true
					s.keys.Close()
					synctest.Wait()
					if n := strings.Count(logs.String(), `"rule":"keylog_loss"`); n != 1 {
						t.Fatalf("storage recovery repeated final summary: %s", logs.String())
					}
				}
			})
		})
	}
}

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
			err := runWithListener(t.Context(), args, &out, &logs, keylog.Open, factory,
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

type progressBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *progressBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *progressBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }
