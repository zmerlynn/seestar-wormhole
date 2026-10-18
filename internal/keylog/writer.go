// Package keylog is the only sink for opt-in capture decryption secrets.
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md
package keylog

import (
	"bytes"
	"errors"
	"fmt"
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

// Writer enqueues bounded lines without waiting for storage. Late writes return
// a closed error and report their loss through the owned logger.
// The command owns it across attempts and calls Close after producers join.
// mu protects queue closure only; no I/O or logging occurs under it.
type Writer struct {
	mu        sync.Mutex
	queue     chan []byte
	closed    bool
	done      chan struct{}
	reported  chan struct{}
	failed    chan struct{}
	dropped   atomic.Uint64
	pending   atomic.Uint64
	timedOut  atomic.Bool
	logger    *slog.Logger
	lastCount atomic.Uint64
	nextWarn  atomic.Int64
	stop      chan struct{}
	stopOnce  sync.Once
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
// A separate interval reporter keeps even a blocked logger off producer paths.
// A nil logger disables diagnostics.
func New(sink io.WriteCloser, logger *slog.Logger) *Writer {
	w := &Writer{queue: make(chan []byte, queueSize), done: make(chan struct{}), reported: make(chan struct{}), failed: make(chan struct{}, 1), logger: logger, stop: make(chan struct{})}
	go func() {
		defer close(w.reported)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-w.failed:
				w.report(false)
			case <-ticker.C:
				w.report(false)
			case <-w.done:
				w.report(true)
				return
			case <-w.stop:
				w.report(true)
				return
			}
		}
	}()
	go func() {
		defer close(w.done)
		for line := range w.queue {
			n, err := sink.Write(line)
			if err != nil || n != len(line) {
				w.drop()
			}
			w.pending.Add(^uint64(0))
			clear(line)
		}
		if sink.Close() != nil {
			w.drop()
		}
	}()
	return w
}

func (w *Writer) report(final bool) {
	if w.logger == nil {
		return
	}
	n := w.dropped.Load()
	previous := w.lastCount.Load()
	now := time.Now().UnixNano()
	next := w.nextWarn.Load()
	if n == previous {
		return
	}
	if !final && (now < next || !w.nextWarn.CompareAndSwap(next, now+int64(time.Second))) {
		return
	}
	if !w.lastCount.CompareAndSwap(previous, n) {
		return
	}
	w.logger.Warn("keylog_failed", "rule", "keylog_loss", "count", n)
}

func (w *Writer) drop() {
	w.dropped.Add(1)
	select {
	case w.failed <- struct{}{}:
	default:
	}
}

// Dropped counts lost lines, abandoned work and final sink-close failures.
// WARN summaries report cumulative counts once per second and at shutdown.
func (w *Writer) Dropped() uint64 { return w.dropped.Load() }

// Write enqueues one complete line, including Pion's unmodified NSS output.
// Full/closed queues and malformed lines return errors; async storage errors
// remain observable through Dropped and Close.
func (w *Writer) Write(p []byte) (int, error) {
	if len(p) == 0 || len(p) > maxLine || p[len(p)-1] != '\n' || bytes.ContainsAny(p[:len(p)-1], "\r\n") {
		w.drop()
		return 0, io.ErrShortWrite
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		w.drop()
		w.report(false)
		return 0, os.ErrClosed
	}
	w.pending.Add(1)
	select {
	case w.queue <- bytes.Clone(p):
		w.mu.Unlock()
	default:
		w.pending.Add(^uint64(0))
		w.mu.Unlock()
		w.drop()
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}

// Close drains for at most ShutdownBound and returns any loss or drain failure.
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
			if w.timedOut.CompareAndSwap(false, true) {
				w.dropped.Add(max(1, w.pending.Load()))
			}
			w.stopOnce.Do(func() { close(w.stop) })
			return errors.New("keylog: drain timeout")
		}
	}
	w.report(true)
	if n := w.Dropped(); n > 0 {
		return fmt.Errorf("keylog: %d diagnostics lost", n)
	}
	return nil
}
