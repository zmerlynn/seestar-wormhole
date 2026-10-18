package keylog

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// lossLogs guards the log buffer the reporter goroutine writes while the test reads it.
type lossLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lossLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lossLogs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func TestKeylogLossReceipt(t *testing.T) {
	for _, mode := range []string{"closed", "queue", "storage", "timeout", "interval"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs lossLogs
				sink := blockedSink{make(chan struct{}, 1), make(chan struct{})}
				var storage io.WriteCloser = sink
				if mode == "closed" || mode == "interval" {
					storage = &recoveringSink{writes: 1}
				}
				if mode == "storage" {
					f, e := os.CreateTemp(t.TempDir(), "keys")
					if e != nil {
						t.Fatal(e)
					}
					f.Close()
					storage = f
				}
				w := New(storage, slog.New(slog.NewJSONHandler(&logs, nil)))
				defer func() { close(sink.release); w.Close() }()
				var receipt error
				if mode == "interval" {
					for range 7 {
						w.Write([]byte("bad"))
					}
					synctest.Wait()
					before := strings.Count(logs.String(), `"level":"WARN"`)
					for range 3 {
						w.Write([]byte("bad"))
					}
					synctest.Wait()
					if before != 1 || strings.Count(logs.String(), `"level":"WARN"`) != 1 {
						t.Fatalf("unbounded warnings: %s", &logs)
					}
					<-time.After(time.Second)
					synctest.Wait()
					if !strings.Contains(logs.String(), `"count":10`) {
						t.Fatalf("interval count missing: %s", &logs)
					}
				} else if mode == "closed" {
					w.Close()
					_, receipt = w.Write([]byte("late\n"))
					if !errors.Is(receipt, os.ErrClosed) {
						t.Fatalf("late receipt=%v", receipt)
					}
				} else {
					w.Write([]byte("key\n"))
					if mode != "storage" {
						<-sink.entered
					}
					if mode == "queue" {
						for range queueSize {
							w.Write([]byte("key\n"))
						}
						_, receipt = w.Write([]byte("lost\n"))
						if receipt == nil {
							t.Fatal("full queue succeeded")
						}
					}
				}
				err := w.Close()
				if mode == "closed" {
					err = w.Close()
				}
				if err == nil || w.Dropped() == 0 {
					t.Fatalf("close=%v dropped=%d", err, w.Dropped())
				}
				synctest.Wait()
				if !strings.Contains(logs.String(), `"rule":"keylog_loss"`) || !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), `"count":`) {
					t.Fatalf("missing loss WARN: %s", &logs)
				}

			})
		})
	}
}
