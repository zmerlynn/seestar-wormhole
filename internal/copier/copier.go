// Package copier carries out a pass's downloads and adoptions in the
// destination.
//
// A [Copier] owns the destination for the life of the process. Every file
// operation under the destination goes through an [os.Root] opened on it, so
// a symbolic link in the destination, at any depth, cannot lead a stat, a
// read, a temporary file, a new folder or a rename outside it.
//
// A download goes to a temporary file in the folder of its final path, named
// by [plan.TempName], so the rename never leaves that folder. The temporary
// file is hashed while it is written, its byte count must equal the listed
// size, and it is fsynced, closed, and given the device's mtime. It is
// then renamed over the final path, its folder is fsynced, and only then is
// its state record appended, with its own fsync. So a crash leaves either
// no record or a complete file with its mtime under its final name, and the
// next pass adopts a file whose record was lost. Go cannot fsync a folder
// on Windows, so there the folder is not fsynced, and a crash may lose a
// recorded file's rename. A failure removes the temporary file; [New]
// removes whatever a crash left behind.
//
// An adoption only reads the local file, and then records it.
//
// The package is named copier, not copy, so that importing it does not
// shadow the builtin copy.
package copier

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// Copier copies files into one destination and records them in its state.
type Copier struct {
	// Copying, if set, is called as a download writes its temporary file,
	// with the share path, the bytes written so far and the listed size:
	// once with 0 bytes when the download starts, then at most about once a
	// second, and once more when it stops, however it stops. Calls for
	// different files can overlap.
	Copying func(path string, done, size int64)

	root  *os.Root
	state *state.State
	now   func() time.Time // the time recorded in a record's at
}

// New opens dest, whose state st the caller holds open (and so locked), and
// removes every temporary file a crash or a failed cleanup left anywhere
// under it. Opening the state first matters: the lock is what keeps a
// second sync from removing the first one's temporary files. A folder below
// dest that New is not allowed to read is skipped with a warning to log (a
// nil log means slog.Default()); any other folder that cannot be read fails
// New, since it may hold a leftover.
func New(dest string, st *state.State, log *slog.Logger) (*Copier, error) {
	readonly.Check("copier.New")
	if log == nil {
		log = slog.Default()
	}
	root, err := os.OpenRoot(dest)
	if err != nil {
		return nil, fmt.Errorf("open destination: %w", err)
	}
	if err := removeTemps(root, log); err != nil {
		root.Close()
		return nil, fmt.Errorf("remove leftover temporary files in %s: %w", dest, err)
	}
	return &Copier{root: root, state: st, now: time.Now}, nil
}

// removeTemps removes each regular file under root whose name
// [plan.IsTempName] accepts. The walk does not follow symbolic links, and
// nothing else is removed: not a link, not a folder, and not a name in
// another case, which the pass decisions refuse but never wrote.
func removeTemps(root *os.Root, log *slog.Logger) error {
	readonly.Check("copier.removeTemps")
	return fs.WalkDir(rootFS(root), ".", func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			// Sync cannot have written in a folder it may not read,
			// such as a lost+found at the top of a mount.
			if p != "." && errors.Is(err, fs.ErrPermission) {
				log.Warn("skipped a folder that cannot be read while removing leftover temporary files", "folder", p, "reason", err)
				return fs.SkipDir
			}
			return err
		}
		if !de.Type().IsRegular() || !plan.IsTempName(de.Name()) {
			return nil
		}
		if err := root.Remove(filepath.FromSlash(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
}

// rootFS returns the file system that [New] walks. Tests replace it to fail
// reading a folder.
var rootFS = (*os.Root).FS

// Close closes the destination. It does not close the state.
func (c *Copier) Close() error {
	return c.root.Close()
}

// Stat returns the local stat of the share path p, without following a
// symbolic link at p itself, for [plan.Input.Local]. A path that does not
// exist has Exists false; a symbolic link in a parent that leads outside the
// destination is an error.
func (c *Copier) Stat(p string) plan.Local {
	return stat(c.root, p)
}

// Dest is a read-only view of a destination, for a dry run. It can only
// stat; unlike a [Copier] it removes no temporary files and needs no state.
type Dest struct {
	root *os.Root // nil if the destination does not exist
}

// OpenDest opens dest for [Dest.Stat]. A destination that does not exist is
// not created; every path in it then does not exist.
func OpenDest(dest string) (*Dest, error) {
	root, err := os.OpenRoot(dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &Dest{}, nil
	case err != nil:
		return nil, fmt.Errorf("open destination: %w", err)
	}
	return &Dest{root: root}, nil
}

// Stat is [Copier.Stat] for the view.
func (d *Dest) Stat(p string) plan.Local {
	if d.root == nil {
		return plan.Local{}
	}
	return stat(d.root, p)
}

// Close closes the view.
func (d *Dest) Close() error {
	if d.root == nil {
		return nil
	}
	return d.root.Close()
}

// stat is [Copier.Stat] under root.
func stat(root *os.Root, p string) plan.Local {
	fi, err := root.Lstat(filepath.FromSlash(p))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return plan.Local{}
	case err != nil:
		return plan.Local{Err: err}
	}
	return plan.Local{Exists: true, Regular: fi.Mode().IsRegular(), Size: fi.Size(), ModTime: fi.ModTime()}
}

// Result is the outcome of one Copy, Replace or Adopt decision.
type Result struct {
	Decision plan.Decision
	// Record is the record appended to the state, with times in UTC. It is
	// the zero Record when Err is set.
	Record state.Record
	// Err says why the file failed. Nothing was recorded for it, and its
	// temporary file was removed. A file that was not started because ctx
	// was done has an Err wrapping ctx's error.
	Err error
	// Elapsed is how long the file took once a worker started it.
	Elapsed time.Duration
}

// Run carries out the Copy, Replace and Adopt decisions in ds, with up to
// workers files at a time (at least one) over the one session sess, and
// calls done with each one's result as soon as that file finishes, so in
// the order files finish. Calls to done never overlap, and Run returns
// after the last. Decisions of other kinds are left out. Each file gets one
// attempt: a failure is in its result and does not stop the others.
//
// A Copy, with or without Overwrite, is recorded as copied and a Replace as
// replaced; both rename over whatever is at the path. An Adopt hashes the
// local file and records it as adopted, without a download. It fails if the
// file changed or another file took its path while it was hashed.
//
// Once ctx is done, reads stop, their temporary files are removed and
// nothing is recorded for them, and files not yet started are not started.
// Run returns when every started file has finished. A file already renamed
// into place is still recorded. Run waits for calls on sess to return; as
// [remote.Session] says, ending the session is what bounds a call the
// server does not answer.
//
// Run must not be called again until it returns.
func (c *Copier) Run(ctx context.Context, sess remote.Session, workers int, ds []plan.Decision, done func(Result)) {
	readonly.Check("copier.Run")
	var jobs []plan.Decision
	for _, d := range ds {
		switch d.Kind {
		case plan.Copy, plan.Replace, plan.Adopt:
			jobs = append(jobs, d)
		}
	}
	// Each worker takes the next job by index; mu keeps calls to done
	// from overlapping.
	var next atomic.Int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range min(max(workers, 1), len(jobs)) {
		wg.Go(func() {
			for {
				i := int(next.Add(1) - 1)
				if i >= len(jobs) {
					return
				}
				r := c.do(ctx, sess, jobs[i])
				mu.Lock()
				done(r)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
}

// do carries out one decision.
func (c *Copier) do(ctx context.Context, sess remote.Session, d plan.Decision) (r Result) {
	r = Result{Decision: d}
	if err := ctx.Err(); err != nil {
		r.Err = fmt.Errorf("not started: %w", err)
		return r
	}
	start := time.Now()
	defer func() { r.Elapsed = time.Since(start) }()
	switch d.Kind {
	case plan.Adopt:
		r.Record, r.Err = c.adopt(ctx, d)
	case plan.Replace:
		r.Record, r.Err = c.download(ctx, sess, d, state.Replaced)
	default:
		r.Record, r.Err = c.download(ctx, sess, d, state.Copied)
	}
	return r
}
