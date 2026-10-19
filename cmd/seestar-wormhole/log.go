package main

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"io"
	"log/slog"
	"sync/atomic"
	"time"
)

func newLogger(format string, w io.Writer) *slog.Logger {
	return diagnostic.Logger(format, w, slog.LevelInfo)
}

// logLossWriter retains errors that slog's void logging calls cannot return.
// It never logs recursively: recovery summaries go straight to the owned sink.
type logLossWriter struct {
	output   io.Writer
	fallback io.Writer
	dropped  atomic.Uint64
	bytes    atomic.Uint64
	reported atomic.Uint64
	nextWarn atomic.Int64
}

func (w *logLossWriter) Fd() uintptr {
	if f, ok := w.output.(interface{ Fd() uintptr }); ok {
		return f.Fd()
	}
	return ^uintptr(0)
}
func (w *logLossWriter) lose(n int) { w.dropped.Add(1); w.bytes.Add(uint64(max(n, 0))) }
func (w *logLossWriter) Dropped() uint64 {
	n := w.dropped.Load()
	if s, ok := w.output.(interface{ Dropped() uint64 }); ok {
		n += s.Dropped()
	}
	return n
}
func (w *logLossWriter) Err() error {
	if w.Dropped() > 0 {
		return errors.New("log: diagnostics lost")
	}
	return nil
}
func (w *logLossWriter) Write(p []byte) (int, error) {
	n, err := w.output.Write(p)
	sink := w.output
	if n != len(p) || err != nil {
		if _, tracked := w.output.(interface{ Dropped() uint64 }); tracked {
			w.bytes.Add(uint64(max(0, len(p)-max(n, 0))))
		} else {
			w.lose(len(p) - max(n, 0))
		}
		if err == nil {
			err = io.ErrShortWrite
		}
		sink = w.fallback
	}
	if w.Dropped() > 0 && w.fallback != nil {
		sink = w.fallback
	}
	w.report(sink)
	return n, err
}

func (w *logLossWriter) report(sink io.Writer) {
	if sink == nil {
		return
	}
	count := w.Dropped()
	now := time.Now().UnixNano()
	next := w.nextWarn.Load()
	if count <= w.reported.Load() || now < next || !w.nextWarn.CompareAndSwap(next, now+int64(time.Second)) {
		return
	}
	r := slog.NewRecord(time.Now(), slog.LevelWarn, "diagnostics_lost", 0)
	r.Add("rule", "log_sink_loss", "count", count, "bytes", w.bytes.Load())
	// This temporary receipt checks short writes without recursively reporting to
	// the failed sink. The original loss remains retained if both outputs fail.
	err := newLogger("json", logReceiptWriter{sink}).Handler().Handle(context.Background(), r)
	if err != nil && w.fallback != nil {
		err = newLogger("json", logReceiptWriter{w.fallback}).Handler().Handle(context.Background(), r)
	}
	if err == nil {
		w.reported.Store(count)
	}
}

// slog handlers inspect the error but do not detect a short write themselves.
type logReceiptWriter struct{ io.Writer }

func (w logReceiptWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if n != len(p) && err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}
