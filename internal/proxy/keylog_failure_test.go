package proxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
)

type faultKeySink struct {
	entered chan struct{}
	release <-chan struct{}
}

func (w faultKeySink) Write(p []byte) (int, error) {
	w.entered <- struct{}{}
	if w.release != nil {
		<-w.release
	}
	return 0, errors.New("secret-path secret-bytes")
}
func (faultKeySink) Close() error { return nil }

// workloadSession uses the production factory/stack DTLS handshake and Pion peer.
func TestKeylogFailureKeepsSessionHealthy(t *testing.T) {

	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "blocked"}[blocked], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs bytes.Buffer
				logger := slog.New(slog.NewTextHandler(&logs, nil))
				sink := faultKeySink{entered: make(chan struct{}, 4)}
				release := make(chan struct{})
				if blocked {
					sink.release = release
					defer close(release)
				}
				keys := keylog.New(sink, logger)
				defer keys.Close()
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				s, _, cloud := workloadSession(t, ctx, func(*simulatedWire) {}, Config{KeyLog: keys, Logger: logger})
				defer s.Close()
				c, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatal("keylog failure broke handshake", err)
				}
				// Fails if the production DTLS factory drops the writer.
				select {
				case <-sink.entered:
				case <-ctx.Done():
					t.Fatal("production DTLS factory lost keylog hook")
				}
				if err := exchange(c.(*stream), 91, 0); err != nil {
					t.Fatal(err)
				}
				c.Close()
				if cloud.allocs.Load() != 1 || cloud.stops.Load() != 0 {
					t.Fatal("keylog changed session/retry state")
				}
				cancel()
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if cloud.stops.Load() != 1 {
					t.Fatal("cloud stop missing")
				}
				start := time.Now()
				keys.Close()
				if time.Since(start) > keylog.ShutdownBound {
					t.Fatal("writer shutdown exceeded bound")
				}
				if !blocked {
					if strings.Count(logs.String(), "keylog_failed") != 1 || !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "count=1") {
						t.Fatal("missing diagnostic", logs.String())
					}
					if strings.Contains(logs.String(), "secret-") {
						t.Fatal("sink error leaked")
					}
				}
			})
		})
	}
}
