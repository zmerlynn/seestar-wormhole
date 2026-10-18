package main

import (
	"io"
	"sync"
	"time"
)

// startupLogOutput holds dependency output through the final configuration
// decision. Encoding remains owned by slog, and storage is capped even while
// cloud selection retries. Once resolved it never retains another record.
type startupLogOutput struct {
	output                        io.Writer
	mu                            sync.Mutex
	resolved, resolving, accepted bool
	pending                       []byte
	lost                          uint64
	lostBytes                     uint64
	err                           error
	nextWarn                      time.Time
}

const startupLogLimit = 64 << 10

func (w *startupLogOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	if !w.resolved {
		if len(p) <= startupLogLimit-len(w.pending) {
			w.pending = append(w.pending, p...)
			w.mu.Unlock()
			return len(p), nil
		}
		w.lose(len(p))
		w.mu.Unlock()
		w.report(false)
		return 0, io.ErrShortWrite
	}
	accepted := w.accepted
	if !accepted {
		w.lose(len(p))
		w.mu.Unlock()
		w.report(false)
		return 0, io.ErrClosedPipe
	}
	w.mu.Unlock()
	n, err := w.output.Write(p)
	if n != len(p) || err != nil {
		w.mu.Lock()
		w.lose(max(0, len(p)-max(0, n)))
		w.mu.Unlock()
		if err == nil {
			err = io.ErrShortWrite
		}
	}
	return n, err
}

// lose is called with mu held. Only counts cross the startup privacy boundary.
func (w *startupLogOutput) lose(n int) { w.lost++; w.lostBytes += uint64(n); w.err = io.ErrShortWrite }
func (w *startupLogOutput) report(final bool) {
	w.mu.Lock()
	count, bytes := w.lost, w.lostBytes
	now := time.Now()
	if count == 0 || (!final && now.Before(w.nextWarn)) {
		w.mu.Unlock()
		return
	}
	w.nextWarn = now.Add(time.Second)
	w.mu.Unlock()
	newLogger("json", w.output).Warn("diagnostics_lost", "rule", "startup_diagnostics_lost", "count", count, "bytes", bytes)
}

func (w *startupLogOutput) resolve(accepted bool) error {
	w.mu.Lock()
	if w.resolved || w.resolving {
		err := w.err
		w.mu.Unlock()
		return err
	}
	w.resolving, w.accepted = true, accepted
	for {
		pending := w.pending
		w.pending = nil
		if !accepted || len(pending) == 0 {
			if len(pending) > 0 {
				w.lose(len(pending))
			}
			w.resolved = true
			err := w.err
			w.mu.Unlock()
			clear(pending)
			return err
		}
		w.mu.Unlock()
		n, err := w.output.Write(pending)
		w.mu.Lock()
		if err != nil || n != len(pending) {
			w.lose(max(0, len(pending)-max(0, n)))
		}
		clear(pending)
	}
}
