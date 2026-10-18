//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"io"
	"os"
	"sync"
	"time"
)

const logQueueSize = 32
const maxLogLine = 16 << 10
const logDrainBound = 250 * time.Millisecond

type failedLog struct{ err error }

func (w failedLog) Write([]byte) (int, error) { return 0, w.err }

// Windows has no sound nonblocking WriteFile equivalent for an inherited
// console/file/anonymous pipe. One worker owns a duplicated handle and a bounded
// queue. Producers drop on overflow; a stalled sink cannot hold session cleanup.
// Close waits only logDrainBound. At most one worker and 33*maxLogLine bytes may
// remain until process exit if Windows never completes the write/close.
type queuedLog struct {
	mu     sync.Mutex // queue closure only; never held for sink I/O
	queue  chan []byte
	done   chan struct{}
	closed bool
}

// consoleLog exposes the worker-owned duplicate only for x/term console queries.
// Writes and handle closure still belong to queuedLog's worker.
type consoleLog struct {
	*queuedLog
	fd uintptr
}

func (w consoleLog) Fd() uintptr { return w.fd }

func newQueuedLog(sink io.WriteCloser) *queuedLog {
	w := &queuedLog{queue: make(chan []byte, logQueueSize), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer sink.Close()
		for b := range w.queue {
			_, _ = sink.Write(b)
		}
	}()
	return w
}

func (w *queuedLog) Write(b []byte) (int, error) {
	if len(b) > maxLogLine {
		return 0, io.ErrShortWrite
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	select {
	case w.queue <- append([]byte(nil), b...):
	default:
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
	}
}

func logWriter() (io.Writer, func()) {
	h, err := windows.GetStdHandle(windows.STD_ERROR_HANDLE)
	if err != nil {
		return failedLog{err}, func() {}
	}
	var duplicate windows.Handle
	process := windows.CurrentProcess()
	if err := windows.DuplicateHandle(process, h, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return failedLog{err}, func() {}
	}
	w := newQueuedLog(os.NewFile(uintptr(duplicate), "stderr"))
	return consoleLog{queuedLog: w, fd: uintptr(duplicate)}, w.close
}
