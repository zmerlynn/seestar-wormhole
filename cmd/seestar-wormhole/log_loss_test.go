package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"testing/synctest"
)

type receiptSink struct {
	bytes.Buffer
	mode string
}

func (s *receiptSink) Write(p []byte) (int, error) {
	if s.mode == "error" {
		return 0, io.ErrClosedPipe
	}
	if s.mode == "short" {
		return len(p) / 2, nil
	}
	return s.Buffer.Write(p)
}

func TestLogLossReceipt(t *testing.T) {
	for _, mode := range []string{"error", "short", "close", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			sink := &receiptSink{mode: mode}
			w := &logLossWriter{output: sink}
			var fallback bytes.Buffer
			if mode == "fallback" {
				sink.mode = "error"
				w.fallback = &fallback
			}
			if mode == "close" {
				w.lose(0)
			} else {
				if n, e := w.Write([]byte("private log\n")); n == 12 || e == nil {
					t.Fatalf("receipt n=%d err=%v", n, e)
				}
			}
			sink.mode = ""
			w.Write([]byte("recovered\n"))
			if w.Err() == nil || w.Dropped() != 1 || !strings.Contains(sink.String()+fallback.String(), `"rule":"log_sink_loss"`) || !strings.Contains(sink.String()+fallback.String(), `"level":"WARN"`) {
				t.Fatalf("loss not retained/reported: %v %s", w.Err(), sink)
			}
			var final bytes.Buffer
			code := mainExitCodeWithRunner([]string{"proxy"}, &final, io.Discard, func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
				return context.Background(), func() {}
			}, func() (io.Writer, func()) { return w, func() {} }, false, func(context.Context, options, io.Writer, io.Writer) error { return nil })
			if code == 0 || !strings.Contains(final.String(), `"rule":"log_sink_loss"`) {
				t.Fatalf("final code=%d output=%s", code, &final)
			}
		})
	}
}

type queueLossSink struct {
	entered, release chan struct{}
	mode             string
}

func (s *queueLossSink) Write(p []byte) (int, error) {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		<-s.release
	}
	if s.mode == "error" {
		return 0, io.ErrClosedPipe
	}
	if s.mode == "short" {
		return 0, nil
	}
	return len(p), nil
}
func (s *queueLossSink) Close() error {
	if s.mode == "close" {
		return io.ErrClosedPipe
	}
	return nil
}
func TestQueuedLogLoss(t *testing.T) {
	for _, mode := range []string{"error", "short", "close", "full", "oversize", "closed", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sink := &queueLossSink{mode: mode}
				if mode == "full" || mode == "timeout" {
					sink.entered = make(chan struct{}, 1)
					sink.release = make(chan struct{})
				}
				w := newQueuedLog(sink)
				w.Write([]byte("first"))
				if sink.entered != nil {
					<-sink.entered
				}
				switch mode {
				case "full":
					for range logQueueSize {
						w.Write([]byte("queued"))
					}
					if n, err := w.Write([]byte("lost")); n != 0 || err == nil {
						t.Fatal("overflow succeeded")
					}
				case "oversize":
					if n, err := w.Write(make([]byte, maxLogLine+1)); n != 0 || err == nil {
						t.Fatal("oversize succeeded")
					}
				}
				w.close()
				if mode == "closed" {
					if n, err := w.Write([]byte("late")); n != 0 || err == nil {
						t.Fatal("closed succeeded")
					}
				}
				if w.Dropped() == 0 {
					t.Fatal("queue loss not counted")
				}
				var final bytes.Buffer
				code := mainExitCodeWithRunner([]string{"proxy"}, &final, io.Discard, func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
					return context.Background(), func() {}
				}, func() (io.Writer, func()) { return w, func() {} }, false, func(context.Context, options, io.Writer, io.Writer) error { return nil })
				if code == 0 || !strings.Contains(final.String(), `"rule":"log_sink_loss"`) || !strings.Contains(final.String(), `"level":"WARN"`) {
					t.Fatalf("queue final code=%d summary=%s", code, &final)
				}
				if sink.release != nil {
					close(sink.release)
					<-w.done
				}
			})
		})
	}
}
