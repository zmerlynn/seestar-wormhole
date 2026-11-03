package app

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"slices"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// The refusals of a Free up space action.
var (
	errFreeBusy = errors.New("a check or a delete is already running")
	errStale    = errors.New("preview again before deleting")
)

// freer runs Free up space: at most one preview or delete at a time. Its
// mutex is never held while waiting on the engine, the disk, the network
// or the log.
type freer struct {
	parent   context.Context
	stateDir string
	log      *slog.Logger
	// dial opens a preview's session and remover a delete's; tests
	// replace them.
	dial    func(ctx context.Context, endpoint string) (remote.Session, error)
	remover func(ctx context.Context, endpoint string) (remote.Remover, error)
	// prepared, if set, runs after a confirm's preparation and before its
	// start; tests change the preview there.
	prepared func()

	mu       sync.Mutex
	cancel   context.CancelFunc // nil when nothing runs
	stop     chan struct{}      // a delete's Stop; nil unless deleting
	done     chan struct{}      // closed when the run has returned
	quitting bool
	previews int // numbers each preview
	st       freeStatus
}

func newFreer(parent context.Context, stateDir string, log *slog.Logger, dial func(context.Context, string) (remote.Session, error)) *freer {
	return &freer{
		parent: parent, stateDir: stateDir, log: log, dial: dial,
		remover: func(ctx context.Context, endpoint string) (remote.Remover, error) {
			return remote.DialRemover(ctx, endpoint)
		},
		st: freeStatus{state: frStart},
	}
}

// options are a run's options for set; the caller adds the rest.
func (f *freer) options(set Settings) (engine.DeleteOptions, error) {
	sel, err := selector(set)
	return engine.DeleteOptions{
		Endpoint: set.Address, Dest: set.Folder, StateDir: f.stateDir,
		TrustState: set.TrustState, OlderThan: time.Duration(set.OlderThanDays) * 24 * time.Hour,
		Selector: sel,
	}, err
}

// run starts o in its own goroutine, with st as the new status, and calls
// end with the outcome once the run has returned. A delete passes its stop
// and the preview id it consumes, which must still be the one shown: the
// check and the move to Deleting are one step under f.mu.
func (f *freer) run(st freeStatus, o engine.DeleteOptions, stop chan struct{}, id int, end func(engine.DeleteResult, error, bool)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done != nil || f.quitting {
		return errFreeBusy
	}
	if id != 0 && (f.st.state != frPreview || f.st.id != id) {
		return errStale
	}
	ctx, cancel := context.WithCancel(f.parent)
	done := make(chan struct{})
	f.cancel, f.stop, f.done, f.st = cancel, stop, done, st
	o.Stop = stop
	go func() {
		r, err := engine.RunDelete(ctx, o, f.log)
		canceled := ctx.Err() != nil
		cancel()
		f.mu.Lock()
		end(r, err, canceled)
		f.cancel, f.stop, f.done = nil, nil, nil
		close(done)
		f.mu.Unlock()
	}()
	return nil
}

// find starts a preview with set, which it keeps.
func (f *freer) find(set Settings) error {
	o, err := f.options(set)
	if err != nil {
		return err
	}
	o.DryRun, o.Dial = true, f.dial
	o.Hooks = engine.Hooks{
		Checked: func(n int) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.st.files = n
		},
		Hashed: func(done, total int64) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.st.hashed, f.st.toHash = done, total
		},
	}
	return f.run(freeStatus{state: frChecking, set: set}, o, nil, 0, func(r engine.DeleteResult, err error, canceled bool) {
		st := &f.st
		switch {
		case canceled || st.stale:
			*st = freeStatus{state: frStart, changed: st.stale && !canceled}
		case err != nil && !errors.Is(err, engine.ErrFailedFiles):
			st.state, st.err = frCheckFail, err
		default:
			f.previews++
			st.preview, st.id, st.state = &r, f.previews, frNothing
			if slices.ContainsFunc(r.Files, func(d engine.DeleteFile) bool { return d.Outcome == engine.FileDeleted }) {
				st.state = frPreview
			}
		}
	})
}

// confirm deletes the files of preview id in the folders named, with the
// preview's settings. Each is checked again before it is removed.
func (f *freer) confirm(id int, folders []string) error {
	f.mu.Lock()
	st := f.st
	f.mu.Unlock()
	if st.state != frPreview || st.id != id {
		return errStale
	}
	only := map[string]bool{}
	for _, d := range st.preview.Files {
		if d.Outcome == engine.FileDeleted && slices.Contains(folders, path.Dir(d.Path)) {
			only[d.Path] = true
		}
	}
	if len(only) == 0 {
		return errors.New("no folder is checked")
	}
	o, err := f.options(st.set)
	if err != nil {
		return err
	}
	o.OpenRemover, o.Only = f.remover, only
	o.Hooks = engine.Hooks{
		Deleted: func(p string, size int64) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.st.deleted++
			f.st.bytes += size
			f.st.current = p
		},
	}
	if f.prepared != nil {
		f.prepared()
	}
	// run checks again that the preview is still the one shown: a Save
	// since the check above has cleared it.
	next := freeStatus{state: frDeleting, set: st.set, total: len(only)}
	return f.run(next, o, make(chan struct{}), id, func(r engine.DeleteResult, err error, _ bool) {
		st := &f.st
		st.result, st.err, st.current = r, err, ""
		switch {
		case err != nil && !errors.Is(err, engine.ErrFailedFiles):
			st.state = frFailed
		case st.stopping && len(r.Files) < st.total:
			st.state = frStopped
		default:
			st.state = frDone
		}
	})
}

// cancelCheck ends a running preview; the tab goes back to Start.
func (f *freer) cancelCheck() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st.state == frChecking && f.cancel != nil {
		f.cancel()
	}
}

// halt stops a delete between files: the file being checked or removed
// finishes and is counted.
func (f *freer) halt() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stop != nil && !f.st.stopping {
		f.st.stopping = true
		close(f.stop)
	}
}

// reset goes back to Start from any state but a running one.
func (f *freer) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done == nil {
		f.st = freeStatus{state: frStart}
	}
}

// clear is a Save that changed what a preview depends on: a preview goes
// back to Start, and a running check ends there.
func (f *freer) clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.st.state {
	case frPreview, frNothing:
		f.st = freeStatus{state: frStart, changed: true}
	case frChecking:
		f.st.stale = true
	}
}

// forget is a clear of the copy history: a preview goes back to Start,
// as for clear. It returns false, and changes nothing, while a check or
// a delete runs.
func (f *freer) forget() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done != nil {
		return false
	}
	if f.st.state == frPreview || f.st.state == frNothing {
		f.st = freeStatus{state: frStart, changed: true}
	}
	return true
}

// quit cancels any run for good and waits for it to return.
func (f *freer) quit() {
	f.mu.Lock()
	f.quitting = true
	if f.cancel != nil {
		f.cancel()
	}
	done := f.done
	f.mu.Unlock()
	if done != nil {
		<-done
	}
}

// view is the status as the page shows it.
func (f *freer) view() freeView {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st.view()
}
