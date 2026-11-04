package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// The Copy tab's preview states, as the page names them. Idle shows no
// preview.
const (
	pvIdle    = ""
	pvRunning = "running"
	pvDone    = "done"
	pvFailed  = "failed"
)

// errPreviewing is a second Preview while one runs.
var errPreviewing = errors.New("a preview is already running")

// previewer runs the Copy tab's Preview: one sync dry run with the saved
// settings, at most one at a time, which changes nothing. Its result is a
// snapshot, which discard drops. Its mutex is never held while waiting on
// the engine, the disk, the network or the log.
type previewer struct {
	parent   context.Context
	stateDir string
	log      *slog.Logger
	// dial opens the preview's session; tests replace it.
	dial func(ctx context.Context, endpoint string) (remote.Session, error)

	mu       sync.Mutex
	cancel   context.CancelFunc // nil when no preview runs
	done     chan struct{}      // closed when the run has returned
	quitting bool
	st       previewStatus
}

// previewStatus is the preview's state, for the page.
type previewStatus struct {
	state    string
	endpoint string
	// Running, and why it failed.
	listing     bool // the session is open
	unreachable bool // the session failed to open
	folders     int
	listed      string // the folder being listed
	err         error
	// Done: what Start would do. Running: files and bytes are those found
	// so far.
	files   int // copies and replacements
	bytes   int64
	groups  []folderGroup
	already int // copied before, or in the folder to adopt
	wait    int
	leftOut int
	failed  int
}

func newPreviewer(parent context.Context, stateDir string, log *slog.Logger, dial func(context.Context, string) (remote.Session, error)) *previewer {
	return &previewer{parent: parent, stateDir: stateDir, log: log, dial: dial}
}

// start runs a preview with set, in its own goroutine.
func (p *previewer) start(set Settings) error {
	sel, err := selector(set)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done != nil {
		return errPreviewing
	}
	if p.quitting {
		return errBusy
	}
	ctx, cancel := context.WithCancel(p.parent)
	done := make(chan struct{})
	p.cancel, p.done = cancel, done
	p.st = previewStatus{state: pvRunning, endpoint: set.Address}
	// The hook runs on the goroutine that calls RunSync, before it
	// returns, so result needs no lock.
	var result *previewStatus
	o := engine.SyncOptions{
		Endpoint: set.Address, Dest: set.Folder, StateDir: p.stateDir, Selector: sel, DryRun: true,
		ReadyAge: readyAge, FirstPassAge: firstPassAge, Workers: workers,
		Dial: func(ctx context.Context, endpoint string) (remote.Session, error) {
			sess, err := p.dial(ctx, endpoint)
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.st.state == pvRunning {
				p.st.listing, p.st.unreachable = err == nil, err != nil
			}
			return sess, err
		},
		Hooks: engine.Hooks{
			Listed: func(f string, n int) {
				p.mu.Lock()
				defer p.mu.Unlock()
				if p.st.state == pvRunning {
					p.st.folders, p.st.listed = n, f
				}
			},
			ToCopy: func(files int, bytes int64) {
				p.mu.Lock()
				defer p.mu.Unlock()
				if p.st.state == pvRunning {
					p.st.files, p.st.bytes = files, bytes
				}
			},
			Decided: func(ds []plan.Decision, out engine.LeftOut) { result = decided(ds, out) },
		},
	}
	go func() {
		// A dry run with failed files returns an error after Decided;
		// the preview shows them.
		err := engine.RunSync(ctx, o, p.log)
		canceled := ctx.Err() != nil
		cancel()
		p.mu.Lock()
		defer p.mu.Unlock()
		st := &p.st
		switch {
		case canceled || st.state != pvRunning:
			// Canceled, or discarded after the run returned.
			*st = previewStatus{}
		case result != nil:
			result.state, result.endpoint = pvDone, st.endpoint
			*st = *result
		default:
			st.state, st.err = pvFailed, err
		}
		p.cancel, p.done = nil, nil
		close(done)
	}()
	return nil
}

// decided is a done preview of a dry run's decisions and what it left out.
func decided(ds []plan.Decision, out engine.LeftOut) *previewStatus {
	st := &previewStatus{leftOut: len(out.Skipped) + out.Unmatched}
	copies := func(d plan.Decision) bool { return d.Kind == plan.Copy || d.Kind == plan.Replace }
	for _, d := range ds {
		switch {
		case copies(d):
			st.files++
			st.bytes += d.Size
		case d.Kind == plan.Done || d.Kind == plan.Adopt:
			st.already++
		case d.Kind == plan.Wait:
			st.wait++
		case d.Kind == plan.Fail:
			st.failed++
		}
	}
	st.groups = groupByFolder(func(yield func(string, int64) bool) {
		for _, d := range ds {
			if copies(d) && !yield(d.Path, d.Size) {
				return
			}
		}
	}, false)
	return st
}

// discard drops the preview, ending one that runs: the settings, the
// history or copying changed what it showed.
func (p *previewer) discard() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	p.st = previewStatus{}
}

// quit cancels any preview for good and waits for it to return.
func (p *previewer) quit() {
	p.mu.Lock()
	p.quitting = true
	if p.cancel != nil {
		p.cancel()
	}
	done := p.done
	p.mu.Unlock()
	if done != nil {
		<-done
	}
}

// previewView is the Copy tab's preview and its text, for the page.
type previewView struct {
	State  string        `json:"state"`
	Title  string        `json:"title"` // also the status region's text
	Text   string        `json:"text,omitempty"`
	Sub    string        `json:"sub,omitempty"`
	Path   string        `json:"path,omitempty"` // what is being looked at
	ToCopy string        `json:"tocopy,omitempty"`
	Groups []folderGroup `json:"groups,omitempty"`
}

// view is the preview as the page shows it; nil when there is none.
func (p *previewer) view() *previewView {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := &p.st
	if st.state == pvIdle {
		return nil
	}
	v := &previewView{State: st.state}
	switch st.state {
	case pvRunning:
		v.Title = "Connecting to " + st.endpoint + "…"
		if st.listing {
			v.Title = "Looking for new files…"
			if st.folders > 0 {
				v.Title += " " + count(st.folders, "folder", "folders") + " checked."
			}
			v.Path = listingLine(st.listed)
			v.ToCopy = toCopyLine(st.files, st.bytes)
		}
	case pvFailed:
		v.Text = "Preview again to retry."
		switch {
		case st.unreachable:
			v.Title = "Can't reach the telescope at " + st.endpoint + "."
			v.Text = checkReach + " " + v.Text
		case st.listing:
			v.Title = "Couldn't read " + unread(st.err) + "."
		default:
			v.Title = "Couldn't preview: " + st.err.Error() + "."
		}
	case pvDone:
		// Files that can't be copied are in the title, which is
		// announced.
		switch {
		case st.files > 0 && st.failed > 0:
			v.Title = fmt.Sprintf("Would copy %s (%s); %s can't be copied.", many(st.files, "file", "files"), size(float64(st.bytes)), num(st.failed))
		case st.files > 0:
			v.Title = fmt.Sprintf("Would copy %s (%s)", many(st.files, "file", "files"), size(float64(st.bytes)))
		case st.failed > 0:
			v.Title = "Preview finished: " + many(st.failed, "file", "files") + " can't be copied."
		default:
			v.Title = "Nothing to copy."
		}
		var parts []string
		for _, c := range []struct {
			n    int
			text string
		}{
			{st.already, "Already copied or in your folder"},
			{st.wait, "Still being written"},
			{st.leftOut, "Left out"},
		} {
			if c.n > 0 {
				parts = append(parts, c.text+" "+num(c.n))
			}
		}
		v.Sub = strings.Join(parts, " · ")
		v.Groups = st.groups
	}
	return v
}
