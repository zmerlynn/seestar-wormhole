package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"golang.org/x/term"
)

const pullProgressLines = 120

// pullProgressOutput coordinates display state without holding mu across sink
// I/O. Exactly one progress writer owns update/clear. Logs serialize only with
// other logs; an in-flight progress Write never delays them. Each display/log
// frame is one Write. Like stderr, the sink must support concurrent log writes
// when a progress Write stalls; ordering those writes cannot be guaranteed.
type pullProgressOutput struct {
	output       io.Writer
	tty          bool
	mu           sync.Mutex // display state only, never held across I/O
	logMu        sync.Mutex // ordinary log serialization, never acquired by progress
	width, lines int
	logging      int
	writing      bool
	// newGrace injects the shutdown clock; nil uses a stoppable time.Timer.
	newGrace func(time.Duration) (<-chan time.Time, func())
}

func newPullProgressOutput(output io.Writer) *pullProgressOutput {
	f, ok := output.(interface{ Fd() uintptr })
	return &pullProgressOutput{output: output, tty: ok && term.IsTerminal(int(f.Fd()))}
}

func (w *pullProgressOutput) clear() {
	w.mu.Lock()
	line := w.clearLine()
	w.width = 0
	w.mu.Unlock()
	if line != "" {
		_, _ = io.WriteString(w.output, line)
	}
}

// clearLine is called only under mu. Clearing and the log record share a Write.
func (w *pullProgressOutput) clearLine() string {
	if w.width == 0 {
		return ""
	}
	return "\r" + strings.Repeat(" ", w.width) + "\r"
}

func (w *pullProgressOutput) Write(p []byte) (int, error) {
	w.logMu.Lock()
	defer w.logMu.Unlock()
	w.mu.Lock()
	prefix := w.clearLine()
	// A pending progress write may land after this log. Retain its width so
	// shutdown (or the next log) can clear it even in that ordering.
	if !w.writing {
		w.width = 0
	}
	w.logging++
	w.mu.Unlock()
	n, err := w.output.Write(append([]byte(prefix), p...))
	w.mu.Lock()
	w.logging--
	w.mu.Unlock()
	return max(0, n-len(prefix)), err
}

func (w *pullProgressOutput) update(s pull.ProgressSnapshot) {
	line := fmt.Sprintf("progress files=%d/%d bytes=%d/%d rate=%.3gB/s", s.FilesDone, s.FilesTotal, s.BytesDone, s.BytesTotal, s.Rate)
	// Query the terminal before taking the display-state lock, too.
	limit := -1
	if w.tty {
		if f, ok := w.output.(interface{ Fd() uintptr }); ok {
			if columns, _, err := term.GetSize(int(f.Fd())); err == nil && columns > 0 {
				// Leave the wrap column unused; aggregate output is ASCII.
				limit = columns - 1
				if len(line) > limit {
					line = line[:limit]
					if limit >= 3 {
						line = line[:limit-3] + "..."
					}
				}
			}
		}
	}
	w.mu.Lock()
	if w.logging > 0 || (!w.tty && w.lines >= pullProgressLines) {
		w.mu.Unlock()
		return
	}
	var frame string
	if w.tty {
		if limit >= 0 {
			w.width = min(w.width, limit)
		}
		padding := strings.Repeat(" ", max(0, w.width-len(line)))
		frame = "\r" + line + padding
		w.width = len(line)
	} else {
		frame = line + "\n"
		w.lines++
	}
	w.writing = true
	w.mu.Unlock()
	_, _ = io.WriteString(w.output, frame)
	w.mu.Lock()
	w.writing = false
	w.mu.Unlock()
}

// start hands snapshots through a size-one latest-value slot. Sampling and
// transfers never wait for output, including the very first update. The sampler
// is always joined. A generic io.Writer.Write cannot be interrupted: shutdown
// joins a healthy writer (including its final clear) or abandons that single
// goroutine after one second to process exit. No further output is queued then.
func (w *pullProgressOutput) start(p *pull.Progress) func() {
	interval := time.Second
	if !w.tty {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	stop, sampled, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	latest := make(chan pull.ProgressSnapshot, 1)
	go func() {
		defer close(sampled)
		defer ticker.Stop()
		for {
			snapshot := p.Snapshot()
			select {
			case <-latest:
			default:
			}
			select {
			case latest <- snapshot:
			default:
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	go func() {
		defer close(done)
		defer w.clear()
		for {
			select {
			case <-stop:
				return
			default:
			}
			select {
			case <-stop:
				return
			case snapshot := <-latest:
				w.update(snapshot)
			}
		}
	}()
	return func() {
		close(stop)
		<-sampled
		// Prefer an already joined writer without allocating a timer.
		select {
		case <-done:
			return
		default:
		}
		newGrace := w.newGrace
		if newGrace == nil {
			newGrace = func(d time.Duration) (<-chan time.Time, func()) {
				timer := time.NewTimer(d)
				return timer.C, func() { timer.Stop() }
			}
		}
		expired, cancel := newGrace(time.Second)
		defer cancel()
		select {
		case <-done:
		case <-expired:
		}
	}
}
