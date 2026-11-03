package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// DeleteOptions are the checked options of a delete run.
type DeleteOptions struct {
	// Endpoint is the SMB host:port of the telescope or a proxy.
	Endpoint string
	// Dest is the local mirror folder whose state, and copies unless
	// TrustState, prove each file. It is never created. Unused with
	// IgnoreState.
	Dest string
	// StateDir is the folder for the state file of Dest. Unused with
	// IgnoreState.
	StateDir string
	// TrustState proves a file by its state record alone.
	TrustState bool
	// IgnoreState reads no state: whatever is selected is deleted.
	IgnoreState bool
	// OlderThan is the least device age of a file to delete; 0 for none.
	OlderThan time.Duration
	Selector  Selector
	// DryRun only reports what would be deleted. It dials with Dial,
	// whose session cannot remove anything.
	DryRun bool
	// Dial opens a dry run's session.
	Dial func(ctx context.Context, endpoint string) (remote.Session, error)
	// OpenRemover opens the session a real run deletes through. The
	// engine never dials one itself: only a caller that passes this can
	// delete, and a test in internal/remote limits who may.
	OpenRemover func(ctx context.Context, endpoint string) (remote.Remover, error)
	// Only, if not nil, limits the run to the files it holds, such as
	// those a preview showed: any other file is left alone, and is neither
	// logged nor counted. Each file in it is still checked as usual. An
	// empty map deletes nothing.
	Only map[string]bool
	// Stop, once closed, ends the run between files: the file being
	// checked or deleted finishes and is counted, and nothing after it
	// starts.
	Stop <-chan struct{}
	// Hooks report the run's progress: Listed, Checked, Hashing, Hashed
	// and Deleted.
	Hooks Hooks
}

// DeleteResult is what a delete run did, or in a dry run would do.
type DeleteResult struct {
	// Files has the outcome of each listed file the run reached, in the
	// order it reached them. A file that Only leaves out has none.
	Files []DeleteFile
	// Folders is how many emptied folders were removed, or would be.
	Folders int
}

// DeleteFile is the outcome of one listed file.
type DeleteFile struct {
	Path    string
	Size    int64
	Outcome Outcome
	// Reason says why a file was kept or failed.
	Reason string
}

// Outcome is what a delete run did with one file.
type Outcome int

// The outcomes of a file.
const (
	// FileDeleted: deleted, or in a dry run, would be.
	FileDeleted Outcome = iota
	// FileKept: a check kept it.
	FileKept
	// FileRecent: younger than OlderThan.
	FileRecent
	// FileFailed: it could not be checked or deleted.
	FileFailed
)

// The proof modes, as the start line names them.
const (
	proofHash  = "hash"  // the record, and the local copy's SHA-256
	proofState = "state" // the record alone: TrustState
	proofNone  = "none"  // no state: IgnoreState
)

func (o *DeleteOptions) proof() string {
	switch {
	case o.IgnoreState:
		return proofNone
	case o.TrustState:
		return proofState
	}
	return proofHash
}

// The reasons a delete run keeps a file for, besides plan's: four from
// checking the local copy, and two from the removal itself.
const (
	KeepMissing    = "local copy missing; give --trust-state if it was moved"
	KeepNotRegular = "local copy is not a regular file"
	KeepSize       = "local copy size differs from the state"
	KeepHash       = "local copy's SHA-256 differs from the state"
	KeepRaced      = "changed on the device during this run"
	KeepGone       = "no longer on the device"
)

// ErrFailedFiles is the end of a run that went through every file but
// failed some locally.
var ErrFailedFiles = errors.New("failed files")

// ErrNoDest is the error for a missing destination when the copies prove
// the files.
var ErrNoDest = errors.New("destination not found; give --trust-state if the copies were moved or the drive is offline")

// RunDelete loads the state, lists the share and deletes what is proven,
// or in a dry run (a preview) reports what it would delete, and returns
// what it did. A stop by ctx or Stop returns a nil error.
func RunDelete(ctx context.Context, o DeleteOptions, log *slog.Logger) (DeleteResult, error) {
	d := &deleter{opts: o, log: log}
	if !o.IgnoreState {
		var err error
		if d.records, err = state.Load(o.StateDir, o.Dest, log); err != nil {
			return DeleteResult{}, err
		}
		if o.proof() == proofHash {
			root, err := os.OpenRoot(o.Dest)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return DeleteResult{}, ErrNoDest
			case err != nil:
				return DeleteResult{}, fmt.Errorf("open destination: %w", err)
			}
			defer root.Close()
			d.dest = root
		}
	}
	log.Info("deletion starting", "proof", o.proof(), "older_than", FormatAge(o.OlderThan), "dry_run", o.DryRun)
	failed, err := d.run(ctx)
	d.result.Folders = d.folders
	switch {
	case ctx.Err() != nil || d.stopped:
		return d.result, nil
	case err != nil:
		return d.result, err
	}
	if o.DryRun {
		log.Info("preview only; add --force to delete")
	}
	switch {
	case failed > 0:
		return d.result, fmt.Errorf("%w: %d", ErrFailedFiles, failed)
	}
	return d.result, nil
}

// deleter is one delete run.
type deleter struct {
	opts DeleteOptions
	log  *slog.Logger
	// records is the loaded state; nil with IgnoreState.
	records map[string]state.Record
	// dest is the destination, open read-only; nil unless the copies
	// prove the files.
	dest *os.Root
	// rm removes files; nil in a dry run.
	rm remote.Remover

	deleted, folders, kept, recent, failed int
	bytes                                  int64
	// hashed and toHash are the bytes of local copies checked so far and
	// in all, for Hooks.Hashed.
	hashed, toHash int64
	// stopped is set when Stop ended the run.
	stopped bool
	result  DeleteResult
}

// run lists the share and goes through it in reverse, so that it reaches a
// folder after everything in it, then logs a summary. It returns how many
// files or folders failed locally; an error ends the run.
func (d *deleter) run(ctx context.Context) (failed int, err error) {
	start := time.Now()
	var left LeftOut
	defer func() {
		names := []string{"deleted", "bytes", "folders"}
		msg := "deletion"
		if d.opts.DryRun {
			names = []string{"delete", "bytes", "remove_folders"}
			msg = "dry run"
		}
		args := []any{names[0], d.deleted, names[1], d.bytes, names[2], d.folders, "kept", d.kept, "recent", d.recent,
			"failed", d.failed, "skipped", len(left.Skipped), "unmatched", left.Unmatched, "elapsed", time.Since(start).Round(time.Millisecond)}
		switch {
		case ctx.Err() != nil || d.stopped:
			d.log.Info(msg+" stopped", args...)
		case err != nil:
			d.log.Error(msg+" failed", append(args, "error", err)...)
		default:
			d.log.Info(msg+" done", args...)
		}
	}()
	var sess remote.Session
	if d.opts.DryRun {
		sess, err = d.opts.Dial(ctx, d.opts.Endpoint)
	} else {
		d.rm, err = d.opts.OpenRemover(ctx, d.opts.Endpoint)
		sess = d.rm
	}
	if err != nil {
		return 0, fmt.Errorf("connect to %s: %w", d.opts.Endpoint, err)
	}
	defer sess.Close()
	entries, left, err := d.opts.Selector.walk(ctx, sess, d.opts.Hooks.Listed)
	skip := "skipped"
	if d.opts.DryRun {
		skip = "would skip"
	}
	for _, x := range left.Skipped {
		d.log.Info(skip, "path", x.Path, "exclude", x.Exclude)
	}
	if err != nil {
		return 0, fmt.Errorf("list the share: %w", err)
	}
	in := plan.DeleteInput{Now: time.Now(), OlderThan: d.opts.OlderThan, ValidPath: remote.ValidPath}
	if d.records != nil {
		in.State = make(map[string]plan.Recorded, len(d.records))
		for p, r := range d.records {
			in.State[p] = plan.Recorded{Size: r.Size, ModTime: r.MTime}
		}
	}
	for _, e := range entries {
		in.Listing = append(in.Listing, plan.Entry{Path: e.Path, Size: e.Size, ModTime: e.ModTime, Regular: e.Kind == remote.Regular})
	}
	decisions := plan.DecideDelete(in)
	files := 0
	for _, e := range entries {
		if e.Kind != remote.Regular || !d.wanted(e.Path) {
			continue
		}
		files++
		if d.dest != nil && decisions[e.Path].Kind == plan.Delete {
			d.toHash += e.Size
		}
	}
	if h := d.opts.Hooks.Checked; h != nil {
		h(files)
	}
	folders := plan.NewFolders(in.Listing, left.keptParents)
	for i := len(entries) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return d.failed, nil
		}
		select {
		case <-d.opts.Stop:
			d.stopped = true
			return d.failed, nil
		default:
		}
		e := entries[i]
		removed := false
		switch e.Kind {
		case remote.Regular:
			removed, err = d.file(ctx, e, decisions[e.Path])
		case remote.Dir:
			if folders.Emptied(e.Path) {
				removed, err = d.folder(ctx, e.Path)
			}
		}
		if err != nil {
			return d.failed, err
		}
		if !removed {
			folders.Kept(e.Path)
		}
	}
	return d.failed, nil
}

// file decides on one listed file and, unless it is kept, deletes it, or
// in a dry run reports that it would. It reports whether the file is gone,
// or would be. An error is a remote failure, which ends the run.
func (d *deleter) file(ctx context.Context, e remote.Entry, dec plan.DeleteDecision) (bool, error) {
	if !d.wanted(e.Path) {
		return false, nil
	}
	switch dec.Kind {
	case plan.Recent:
		d.recent++
		d.add(e, FileRecent, "")
		return false, nil
	case plan.Keep:
		d.keep(e, dec.Reason, dec.Warn)
		return false, nil
	}
	if d.dest != nil {
		if h := d.opts.Hooks.Hashing; h != nil {
			h(e.Path)
		}
		reason, warn, n, err := proveFile(ctx, d.dest, e.Path, d.records[e.Path])
		d.hashed += n
		if h := d.opts.Hooks.Hashed; h != nil {
			h(d.hashed, d.toHash)
		}
		switch {
		case ctx.Err() != nil:
			return false, nil
		case err != nil:
			// A local failure fails the file, and the run goes on.
			d.fail(e, err)
			return false, nil
		case reason != "":
			d.keep(e, reason, warn)
			return false, nil
		}
	}
	if d.opts.DryRun {
		d.deleted++
		d.bytes += e.Size
		d.add(e, FileDeleted, "")
		d.log.Info("would delete", "path", e.Path, "size", e.Size)
		return true, nil
	}
	err := deleteFile(ctx, d.rm, e)
	switch {
	case err == nil:
		d.deleted++
		d.bytes += e.Size
		d.add(e, FileDeleted, "")
		d.log.Info("file deleted", "path", e.Path, "size", e.Size)
		if h := d.opts.Hooks.Deleted; h != nil {
			h(e.Path, e.Size)
		}
		return true, nil
	case ctx.Err() != nil:
		return false, nil
	case errors.Is(err, remote.ErrChanged):
		d.keep(e, KeepRaced, true)
		return false, nil
	case errors.Is(err, fs.ErrNotExist):
		d.keep(e, KeepGone, false)
		return false, nil
	}
	d.fail(e, err)
	return false, err
}

// wanted reports whether Only lets the run decide on the file p.
func (d *deleter) wanted(p string) bool {
	return d.opts.Only == nil || d.opts.Only[p]
}

// add adds the outcome of e to the result.
func (d *deleter) add(e remote.Entry, o Outcome, reason string) {
	d.result.Files = append(d.result.Files, DeleteFile{Path: e.Path, Size: e.Size, Outcome: o, Reason: reason})
}

func (d *deleter) keep(e remote.Entry, reason string, warn bool) {
	d.kept++
	d.add(e, FileKept, reason)
	level := slog.LevelInfo
	if warn {
		level = slog.LevelWarn
	}
	d.log.Log(context.Background(), level, "file kept", "path", e.Path, "reason", reason)
}

func (d *deleter) fail(e remote.Entry, err error) {
	d.failed++
	d.add(e, FileFailed, err.Error())
	d.log.Warn("file failed", "path", e.Path, "reason", err)
}

// folder removes a folder the run emptied, or in a dry run reports that it
// would. It reports whether the folder is gone, or would be. An error is a
// remote failure, which ends the run.
func (d *deleter) folder(ctx context.Context, p string) (bool, error) {
	if d.opts.DryRun {
		d.folders++
		d.log.Info("would remove folder", "path", p)
		return true, nil
	}
	err := removeFolder(ctx, d.rm, p)
	switch {
	case err == nil:
		d.folders++
		d.log.Info("folder removed", "path", p)
		return true, nil
	case ctx.Err() != nil:
		return false, nil
	case errors.Is(err, remote.ErrNotEmpty):
		d.log.Info("folder kept", "path", p, "reason", "not empty: something was added during the run")
		return false, nil
	case errors.Is(err, remote.ErrChanged):
		d.log.Warn("folder kept", "path", p, "reason", "changed on the device during this run")
		return false, nil
	case errors.Is(err, fs.ErrNotExist):
		d.log.Info("folder kept", "path", p, "reason", "no longer on the device")
		return false, nil
	}
	d.failed++
	d.log.Warn("folder failed", "path", p, "reason", err)
	return false, err
}

// proveFile checks the local copy of share path p against its record: it
// must be a regular file of the recorded size whose SHA-256 is the
// recorded one. It returns the reason to keep the file, if any, with
// whether that is worth a warning, or an error if the copy could not be
// read, and the bytes it read. It only reads.
func proveFile(ctx context.Context, dest *os.Root, p string, rec state.Record) (reason string, warn bool, n int64, err error) {
	name := filepath.FromSlash(p)
	fi, err := dest.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return KeepMissing, false, 0, nil
	case err != nil:
		return "", false, 0, err
	case !fi.Mode().IsRegular():
		return KeepNotRegular, true, 0, nil
	case fi.Size() != rec.Size:
		return KeepSize, true, 0, nil
	}
	f, err := dest.Open(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return KeepMissing, false, 0, nil
	case err != nil:
		return "", false, 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err = io.Copy(h, ctxReader{ctx, f})
	switch {
	case err != nil:
		return "", false, n, err
	case hex.EncodeToString(h.Sum(nil)) != rec.SHA256:
		return KeepHash, true, n, nil
	}
	return "", false, n, nil
}

// ctxReader stops reading once ctx is done, so a stop ends a long hash.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// deleteFile removes e from the device. A dry run never reaches it.
func deleteFile(ctx context.Context, rm remote.Remover, e remote.Entry) error {
	readonly.Check("deleteFile")
	return rm.RemoveFile(ctx, e)
}

// removeFolder removes the empty folder p from the device. A dry run never
// reaches it.
func removeFolder(ctx context.Context, rm remote.Remover, p string) error {
	readonly.Check("removeFolder")
	return rm.RemoveDir(ctx, p)
}

// day is the unit of a whole-days age.
const day = 24 * time.Hour

// FormatAge prints an age limit as the delete log names it: a whole number
// of days as days, such as 7d, zero as 0, and any other as a Go duration.
func FormatAge(d time.Duration) string {
	switch {
	case d == 0:
		return "0"
	case d%day == 0:
		return strconv.FormatInt(int64(d/day), 10) + "d"
	}
	return d.String()
}
