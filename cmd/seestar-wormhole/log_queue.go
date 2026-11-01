package main

import (
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const logQueueSize = 32

// A 4096-byte serial can expand to 20 KiB after display and log escaping.
const maxLogLine = 32 << 10
const logDrainBound = 250 * time.Millisecond

// Windows has no sound nonblocking WriteFile equivalent for an inherited
// console/file/anonymous pipe. One worker owns a duplicated handle and a bounded
// queue. Producers drop on overflow; a stalled sink cannot hold session cleanup.
// Close waits only logDrainBound. At most one worker and 33*maxLogLine bytes may
// remain until process exit if Windows never completes the write/close.
type queuedLog struct {
	mu       sync.Mutex // queue closure only; never held for sink I/O
	queue    chan []byte
	done     chan struct{}
	closed   bool
	reporter *logLossWriter
	dropped  atomic.Uint64
}

func newQueuedLog(sink io.WriteCloser) *queuedLog {
	w := &queuedLog{queue: make(chan []byte, logQueueSize), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer func() {
			if sink.Close() != nil {
				w.dropped.Add(1)
				w.report(nil)
			}
		}()
		for b := range w.queue {
			if n, err := sink.Write(b); err != nil || n != len(b) {
				w.dropped.Add(1)
				w.report(nil)
			} else {
				w.report(sink)
			}
		}
	}()
	return w
}

func (w *queuedLog) Write(b []byte) (int, error) {
	if len(b) > maxLogLine {
		w.dropped.Add(1)
		return 0, io.ErrShortWrite
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		w.dropped.Add(1)
		return 0, os.ErrClosed
	}
	select {
	case w.queue <- append([]byte(nil), b...):
	default:
		w.dropped.Add(1)
		return 0, io.ErrShortWrite
	}
	return len(b), nil
}

func (w *queuedLog) close() {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()
	timer := time.NewTimer(logDrainBound)
	defer timer.Stop()
	select {
	case <-w.done:
	case <-timer.C:
		w.dropped.Add(uint64(len(w.queue)) + 1)
	}
}

func (w *queuedLog) Dropped() uint64 { return w.dropped.Load() }

func (w *queuedLog) setLogReporter(reporter *logLossWriter) {
	w.mu.Lock()
	w.reporter = reporter
	w.mu.Unlock()
}
func (w *queuedLog) report(sink io.Writer) {
	w.mu.Lock()
	reporter := w.reporter
	w.mu.Unlock()
	if reporter == nil {
		return
	}
	if reporter.fallback != nil {
		sink = reporter.fallback
	}
	reporter.report(sink)
}
