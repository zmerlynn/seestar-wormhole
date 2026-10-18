package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
)

// startupKeylogHandler holds only the keylog worker's one-shot failure record.
// It avoids buffering general startup logs or losing a warning emitted before
// startup is accepted. The worker uses this logger directly, without With calls.
type startupKeylogHandler struct {
	slog.Handler
	// mu protects resolution and the single pending record; output happens unlocked.
	mu                 sync.Mutex
	resolved, accepted bool
	pending            *slog.Record
}

func (h *startupKeylogHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	if !h.resolved {
		record := r.Clone()
		h.pending = &record
		h.mu.Unlock()
		return nil
	}
	accepted := h.accepted
	h.mu.Unlock()
	if accepted {
		return h.Handler.Handle(ctx, r)
	}
	return nil
}

func (h *startupKeylogHandler) resolve(accepted bool) {
	h.mu.Lock()
	if h.resolved {
		h.mu.Unlock()
		return
	}
	h.resolved, h.accepted = true, accepted
	pending := h.pending
	h.pending = nil
	h.mu.Unlock()
	if accepted && pending != nil {
		_ = h.Handler.Handle(context.Background(), *pending)
	}
}

// startupLogOutput holds dependency output through the final configuration
// decision. Encoding remains owned by slog, and storage is capped even while
// cloud selection retries. Once resolved it never retains another record.
type startupLogOutput struct {
	output                        io.Writer
	mu                            sync.Mutex
	resolved, resolving, accepted bool
	pending                       []byte
}

const startupLogLimit = 64 << 10

func (w *startupLogOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	if !w.resolved {
		if len(p) <= startupLogLimit-len(w.pending) {
			w.pending = append(w.pending, p...)
		}
		w.mu.Unlock()
		return len(p), nil
	}
	accepted := w.accepted
	w.mu.Unlock()
	if accepted {
		return w.output.Write(p)
	}
	return len(p), nil
}

func (w *startupLogOutput) resolve(accepted bool) {
	w.mu.Lock()
	if w.resolved || w.resolving {
		w.mu.Unlock()
		return
	}
	w.resolving, w.accepted = true, accepted
	for {
		pending := w.pending
		w.pending = nil
		if !accepted || len(pending) == 0 {
			w.resolved = true
			w.mu.Unlock()
			clear(pending)
			return
		}
		// Writes still buffer during the drain, so flushing cannot race live
		// output or invert startup records. No state mutex spans sink I/O.
		w.mu.Unlock()
		_, _ = w.output.Write(pending)
		clear(pending)
		w.mu.Lock()
	}
}
