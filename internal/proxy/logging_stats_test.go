package proxy

import (
	"bytes"
	"context"
	"github.com/go-json-experiment/json"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// Virtual time exercises the real timer worker with a quiet open channel.
func TestIdleLoggingLevels(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		t.Run(level.String(), func(t *testing.T) {
			t.Setenv("SEESTAR_EMAIL", "")
			t.Setenv("SEESTAR_PASSWORD", "")
			synctest.Test(t, func(t *testing.T) {
				var logs statsLogBuffer
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s, _, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{StatsInterval: time.Second, Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: level}))})
				c, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				logs.take()
				<-time.NewTimer(time.Minute).C
				synctest.Wait()
				data := logs.take()
				lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
				counts := map[string]int{}
				for _, line := range lines {
					if len(line) == 0 {
						continue
					}
					var record struct {
						Message string `json:"msg"`
						Level   string `json:"level"`
					}
					if err := json.Unmarshal(line, &record, json.MatchCaseInsensitiveNames(true)); err != nil {
						t.Fatal(err)
					}
					if record.Message == "transport_discard_summary" {
						if record.Level != "WARN" {
							t.Errorf("discard summary level=%s want WARN", record.Level)
						}
						continue
					}
					counts[record.Message]++
					if level == slog.LevelDebug && (record.Message == "transport_stats" || record.Message == "rdt_stats" || record.Message == "tunnel_stats") && record.Level != "DEBUG" {
						t.Errorf("stats level=%s want DEBUG", record.Level)
					}
				}
				if level == slog.LevelInfo {
					if len(counts) != 0 {
						t.Fatalf("idle INFO wrote non-summary records; want 0: %s", data)
					}
				} else {
					for _, event := range []string{"transport_stats", "rdt_stats", "tunnel_stats"} {
						if counts[event] != 60 {
							t.Errorf("%s=%d/minute want 60", event, counts[event])
						}
					}
				}
				c.Close()
				s.Close()
			})
		})
	}
}

// The live timer owner can become runnable between virtual-time barriers.
// Snapshots must synchronize with the handler's concurrent writes.
type statsLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *statsLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *statsLogBuffer) take() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := bytes.Clone(b.buffer.Bytes())
	b.buffer.Reset()
	return p
}
