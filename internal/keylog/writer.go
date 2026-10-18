// Package keylog is the only sink for opt-in capture decryption secrets.
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md
package keylog

import (
	"bytes"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const queueSize = 32
const maxLine = 256

// ShutdownBound limits the command's final diagnostic drain, including Close.
const ShutdownBound = 2 * time.Second

// Writer accepts best-effort lines without waiting for storage or diagnostics.
// The command owns it across attempts and calls Close after producers join.
// mu protects queue closure only; no I/O or logging occurs under it.
type Writer struct {
	mu       sync.Mutex
	queue    chan []byte
	closed   bool
	done     chan struct{}
	reported chan struct{}
	failed   chan struct{}
	dropped  atomic.Uint64
}

// Open creates a new private file; existing files (including symlinks) are refused.
func Open(path string, logger *slog.Logger) (*Writer, error) {
	f, err := privatefile.Open(path, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return nil, errors.New("keylog: cannot create private file")
	}
	return New(f, logger), nil
}

// New starts one storage worker, taking ownership of an unbuffered sink.
// Each line is written immediately, with no userspace buffering or per-line fsync.
// A separate one-shot reporter keeps even a blocked logger off producer paths.
// A nil logger disables diagnostics.
func New(sink io.WriteCloser, logger *slog.Logger) *Writer {
	w := &Writer{queue: make(chan []byte, queueSize), done: make(chan struct{}), reported: make(chan struct{}), failed: make(chan struct{}, 1)}
	go func() {
		defer close(w.reported)
		select {
		case <-w.failed:
		case <-w.done:
		}
		if n := w.dropped.Load(); n > 0 && logger != nil {
			logger.Warn("keylog_failed", "count", n)
		}
	}()
	go func() {
		defer close(w.done)
		for line := range w.queue {
			n, err := sink.Write(line)
			if err != nil || n != len(line) {
				w.drop()
			}
			clear(line)
		}
		if sink.Close() != nil {
			w.drop()
		}
	}()
	return w
}

func (w *Writer) drop() {
	w.dropped.Add(1)
	select {
	case w.failed <- struct{}{}:
	default:
	}
}

// Dropped counts lost lines (and final sink-close failures). The one warning
// reports the count observed at first notification; this counter keeps growing.
func (w *Writer) Dropped() uint64 { return w.dropped.Load() }

// Write enqueues one complete line, including Pion's unmodified NSS output.
// Full/closed queues, malformed lines and storage failures never reach callers.
func (w *Writer) Write(p []byte) (int, error) {
	if len(p) == 0 || len(p) > maxLine || p[len(p)-1] != '\n' || bytes.ContainsAny(p[:len(p)-1], "\r\n") {
		w.drop()
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		w.drop()
		return len(p), nil
	}
	select {
	case w.queue <- bytes.Clone(p):
	default:
		w.drop()
	}
	return len(p), nil
}

// Close drains for at most ShutdownBound and never returns a diagnostic error.
// A stalled syscall may retain the single worker and its bounded queue until
// storage recovers or the process exits. It cannot hold session/cloud cleanup.
func (w *Writer) Close() error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()
	timer := time.NewTimer(ShutdownBound)
	defer timer.Stop()
	for _, done := range []<-chan struct{}{w.done, w.reported} {
		select {
		case <-done:
		case <-timer.C:
			return nil
		}
	}
	return nil
}
