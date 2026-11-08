package app

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"slices"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/engine"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/guard"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
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
	stateDir string // the default history folder; Settings may choose another
	log      *slog.Logger
	// dial opens a preview's session and remover a delete's; tests
	// replace them.
	dial    func(ctx context.Context, t Target) (remote.Session, error)
	remover func(ctx context.Context, t Target) (remote.Remover, error)
	// at, if set, fails each dial and remover once the telescope is no
	// longer at the target the run started at: a [Mount]'s.
	at func(t Target) error
	// prepared, if set, runs after a confirm's preparation and before its
	// start; tests change the preview there.
	prepared func()
	// record, if set, logs each run's outcome to Activity as "delete".
	record *slog.Logger

	mu       sync.Mutex
	cancel   context.CancelFunc // nil when nothing runs
	stop     chan struct{}      // a delete's Stop; nil unless deleting
	done     chan struct{}      // closed when the run has returned
	quitting bool
	previews int // numbers each preview
	st       freeStatus
}

func newFreer(parent context.Context, stateDir string, log *slog.Logger, dial func(context.Context, Target) (remote.Session, error)) *freer {
	return &freer{
		parent: parent, stateDir: stateDir, log: log, dial: dial,
		remover: func(ctx context.Context, t Target) (remote.Remover, error) {
			return remote.DialRemover(ctx, t.Endpoint, log)
		},
		st: freeStatus{state: frStart},
	}
}

// options are a run's options for set; the caller adds the rest. Its
// selection is Free up space's own patterns, after the built-in excludes,
// as `delete --exclude --include` selects; Copy's patterns play no part.
func (f *freer) options(set Settings) (engine.DeleteOptions, error) {
	sel, err := selector(Settings{Excludes: set.FreeExcludes, Includes: set.FreeIncludes, SkipBuiltIn: true})
	return engine.DeleteOptions{
		Endpoint: set.Address, Dest: set.Folder, StateDir: set.historyDir(f.stateDir),
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
		defer guard.Recover(f.log, "freeing up space")
		var (
			r        engine.DeleteResult
			err      error
			canceled bool
		)
		// The run is over, and done closes, on every exit, a recovered
		// panic in end's included, so Quit and the next run never wait.
		defer func() {
			cancel()
			f.mu.Lock()
			defer f.mu.Unlock()
			defer func() {
				f.cancel, f.stop, f.done = nil, nil, nil
				close(done)
			}()
			defer guard.Recover(f.log, "freeing up space")
			end(r, err, canceled)
		}()
		err = guard.Run(f.log, "freeing up space", func() (err error) {
			r, err = engine.RunDelete(ctx, o, f.log)
			return err
		})
		canceled = ctx.Err() != nil
		f.outcome(o.DryRun, r, err, canceled)
	}()
	return nil
}

// outcome logs a run's outcome to record, without f.mu held.
func (f *freer) outcome(dry bool, r engine.DeleteResult, err error, canceled bool) {
	if f.record == nil {
		return
	}
	var n, failed int
	var bytes int64
	for _, d := range r.Files {
		switch d.Outcome {
		case engine.FileDeleted:
			n, bytes = n+1, bytes+d.Size
		case engine.FileFailed:
			failed++
		}
	}
	verb := "deleted"
	if dry {
		verb = "can delete"
	}
	switch {
	case canceled && dry:
		f.record.Info("free up space preview canceled")
	case err != nil && !errors.Is(err, engine.ErrFailedFiles):
		f.record.Warn("free up space failed", "dry_run", dry, "err", err)
	case failed > 0:
		f.record.Warn("free up space "+verb, "files", n, "size", size(float64(bytes)), "failed", failed)
	default:
		f.record.Info("free up space "+verb, "files", n, "size", size(float64(bytes)))
	}
}

// find starts a preview with set at t, which it keeps.
func (f *freer) find(set Settings, t Target) error {
	o, err := f.options(set)
	if err != nil {
		return err
	}
	o.DryRun, o.Hooks = true, f.hooks(nil)
	o.Dial = func(ctx context.Context, _ string) (remote.Session, error) {
		return establish(ctx, t, func(ctx context.Context) (remote.Session, error) {
			if err := f.still(t); err != nil {
				return nil, err
			}
			return f.dial(ctx, t)
		})
	}
	return f.run(freeStatus{state: frChecking, set: set, target: t}, o, nil, 0, func(r engine.DeleteResult, err error, canceled bool) {
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
// preview's settings, if now, where the telescope is now, is the target
// the preview was checked at, in the same run. Each file is checked again
// before it is removed.
func (f *freer) confirm(id int, folders []string, now Target) error {
	f.mu.Lock()
	st := f.st
	f.mu.Unlock()
	if st.state != frPreview || st.id != id || st.target != now {
		return errStale
	}
	only, checked := map[string]bool{}, map[string]bool{}
	for _, d := range st.preview.Files {
		if dir := path.Dir(d.Path); d.Outcome == engine.FileDeleted && slices.Contains(folders, dir) {
			only[d.Path], checked[dir] = true, true
		}
	}
	if len(only) == 0 {
		return errors.New("no folder is checked")
	}
	o, err := f.options(st.set)
	if err != nil {
		return err
	}
	o.Only, o.Hooks = only, f.hooks(checked)
	// Only opening the session is tried again; nothing is deleted until it
	// is open.
	o.OpenRemover = func(ctx context.Context, _ string) (remote.Remover, error) {
		return establish(ctx, st.target, func(ctx context.Context) (remote.Remover, error) {
			if err := f.still(st.target); err != nil {
				return nil, err
			}
			return f.remover(ctx, st.target)
		})
	}
	if f.prepared != nil {
		f.prepared()
	}
	// run checks again that the preview is still the one shown: a Save
	// since the check above has cleared it.
	next := freeStatus{state: frDeleting, set: st.set, target: st.target, total: len(only), rechecking: true}
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

// still fails if the telescope is no longer at t.
func (f *freer) still(t Target) error {
	if f.at == nil {
		return nil
	}
	return f.at(t)
}

// hooks keep a preview's or a delete's status: its counts, and in
// current the one line saying what it is looking at, cleared once that is
// done. The title alone is announced. A delete passes the folders checked:
// its listing then names only those, without a count.
func (f *freer) hooks(checked map[string]bool) engine.Hooks {
	return engine.Hooks{
		Listed: func(p string, n int) {
			f.mu.Lock()
			defer f.mu.Unlock()
			switch {
			case checked == nil:
				f.st.current = listingLine(p) + " (" + many(n, "folder", "folders") + ")"
			case checked[p]:
				f.st.current = listingLine(p)
			}
		},
		Checked: func(n int) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.st.files, f.st.current, f.st.rechecking = n, "", false
		},
		Hashing: func(p string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.st.current = "Checking " + p + "…"
		},
		Hashed: func(done, total int64) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.st.hashed, f.st.toHash, f.st.current = done, total, ""
		},
		Deleted: func(p string, size int64) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.st.deleted++
			f.st.bytes += size
			f.st.current = "Last deleted: " + p
		},
	}
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

// running reports whether a check or a delete runs.
func (f *freer) running() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done != nil
}

// reset goes back to Start from any state but a running one.
func (f *freer) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done == nil {
		f.st = freeStatus{state: frStart}
	}
}

// clear is a Save, or a change of where the telescope is, that changed
// what a preview depends on: a preview or a failed check goes back to
// Start, and a running check ends there.
func (f *freer) clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.st.state {
	case frPreview, frNothing:
		f.st = freeStatus{state: frStart, changed: true}
	case frCheckFail:
		// Its error was from before; there was no preview to change.
		f.st = freeStatus{state: frStart}
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
