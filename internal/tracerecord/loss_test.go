package tracerecord

import (
	"bytes"
	"io"
	"log/slog"
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

type lossSink struct {
	bytes.Buffer
	mode             string
	entered, release chan struct{}
}

func (s *lossSink) Write(p []byte) (int, error) {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		<-s.release
	}
	if s.mode == "storage" {
		return 0, io.ErrClosedPipe
	}
	if s.mode == "short" {
		return 1, nil
	}
	return s.Buffer.Write(p)
}
func (s *lossSink) Close() error {
	if s.mode == "close" {
		return io.ErrClosedPipe
	}
	return nil
}
func TestTraceLossReceipt(t *testing.T) {
	for _, mode := range []string{"storage", "short", "close", "cap", "closed", "invalid", "outputs", "limit", "queue", "timeout", "ports_late", "ports_cap", "ports_zero", "interval"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs lossLogs
				sink := &lossSink{mode: mode}
				cap := SizeCap
				if mode == "cap" {
					cap = 1024
				}
				if mode == "queue" || mode == "timeout" {
					sink.entered = make(chan struct{}, 1)
					sink.release = make(chan struct{})
				}
				w := New(sink, cap, time.Now)
				defer func() {
					if sink.release != nil {
						close(sink.release)
					}
					w.Close()
				}()
				w.SetLogger(slog.New(slog.NewJSONHandler(&logs, nil)))
				var configErr error
				switch mode {
				case "interval":
					for range 7 {
						w.Record("unknown", 1, 0, EdgeEvent{}, []EdgeEvent{})
					}
					synctest.Wait()
					for range 3 {
						w.Record("unknown", 1, 0, EdgeEvent{}, []EdgeEvent{})
					}
					synctest.Wait()
					if strings.Count(logs.String(), `"level":"WARN"`) != 1 {
						t.Fatalf("unbounded warnings: %s", &logs)
					}
					<-time.After(time.Second)
					synctest.Wait()
					if !strings.Contains(logs.String(), `"count":10`) {
						t.Fatalf("interval count missing: %s", &logs)
					}
				case "closed":
					w.Close()
					w.Record("dtls", 1, 0, EdgeEvent{}, []EdgeEvent{})
				case "invalid":
					w.Record("unknown", 1, 0, EdgeEvent{}, []EdgeEvent{})
				case "outputs":
					w.Record("dtls", 1, 0, EdgeEvent{}, 123)
				case "limit":
					w.Record("dtls", 1, 0, EdgeEvent{}, make([]EdgeEvent, 2049))
				case "ports_cap":
					configErr = w.SetServicePorts(make([]uint16, 129))
				case "ports_zero":
					configErr = w.SetServicePorts([]uint16{0})
				default:
					w.Record("dtls", 1, 0, EdgeEvent{}, []EdgeEvent{})
					if mode == "ports_late" {
						configErr = w.SetServicePorts([]uint16{445})
					}
					if sink.entered != nil {
						<-sink.entered
					}
					if mode == "queue" {
						for range queueCapacity + 1 {
							w.Record("dtls", 1, 0, EdgeEvent{}, []EdgeEvent{})
						}
					}
				}
				err := w.Close()
				synctest.Wait()
				if strings.HasPrefix(mode, "ports_") && configErr == nil {
					t.Fatal("configuration omission succeeded")
				}
				if err == nil || w.Dropped() == 0 {
					t.Fatalf("close=%v lost=%d", err, w.Dropped())
				}
				if !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), `"rule":"trace_loss"`) || !strings.Contains(logs.String(), `"count":`) {
					t.Fatalf("missing loss WARN: %s", &logs)
				}

			})
		})
	}
}
