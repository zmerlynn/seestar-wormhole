package main

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
	"time"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

type deleteOptions struct {
	endpoint    string
	dest        string
	destSet     bool
	stateDir    string
	stateDirSet bool
	trustState  bool
	ignoreState bool
	olderThan   time.Duration
	selection   selectionFlags
	// selector is set by validate from selection.
	selector selector
	// dryRun is set by --dry-run, and by validate when --force is not
	// given: either way the run only previews.
	dryRun bool
	force  bool
}

// The proof modes, as the start line names them.
const (
	proofHash  = "hash"  // the record, and the local copy's SHA-256
	proofState = "state" // the record alone: --trust-state
	proofNone  = "none"  // no state: --ignore-state
)

func (o *deleteOptions) proof() string {
	switch {
	case o.ignoreState:
		return proofNone
	case o.trustState:
		return proofState
	}
	return proofHash
}

func newDeleteCommand(log *slog.Logger) *cobra.Command {
	o := deleteOptions{olderThan: 7 * day}
	cmd := &cobra.Command{
		Use:   "delete --endpoint HOST:PORT --dest DIR [flags]",
		Short: "Delete files from the telescope's share once they are copied",
		Long: `Delete files from the telescope's share once they are copied.
delete is the one command that changes the telescope's share. Without
--force it only previews, as --dry-run does: it lists the share, reports
what it would delete and changes nothing. With --force it deletes, without
asking. --force only means act; it never weakens the proof below.
By default a file is deleted only if the state file of --dest records it
with the size and mtime the device lists, and its copy in --dest still has
the recorded SHA-256. With --trust-state the record is enough and the copy
is not read: use it when the copies were moved out of --dest or the drive
is offline. With --ignore-state no state is read, and whatever is selected
is deleted, whether or not it was copied; at least one --include is
required with it.
Only files whose device mtime is at least --older-than old are deleted.
--exclude, --no-default-excludes and --include select exactly as for sync
(see ` + programName + ` help sync). A folder that held entries and that
the run left empty is removed too; the share root never is.
Do not run delete while the telescope is capturing, restacking or managing
files in its app: delete checks each file just before it removes it, but a
file rewritten between that check and the removal is lost.
Durations use Go units, such as 36h, or whole days, such as 7d.
Logs and errors go to stderr.
Exit 0 when the run finishes, however many files it kept, or is stopped;
1 on a runtime failure or failed files; 2 on a usage error.`,
		Example: "  " + programName + " delete --endpoint 127.0.0.1:4445 --dest ~/Seestar\n" +
			"  " + programName + " delete --endpoint 127.0.0.1:4445 --dest ~/Seestar --force\n" +
			"  " + programName + " delete --endpoint 127.0.0.1:4445 --ignore-state --include '*.jpg' --older-than 30d --force",
		Args: noArguments,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.destSet = cmd.Flags().Changed("dest")
			o.stateDirSet = cmd.Flags().Changed("state-dir")
			if err := o.validate(); err != nil {
				return err
			}
			return runDelete(cmd.Context(), o, log)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.endpoint, "endpoint", "", "SMB host:port of the telescope or a proxy (required)")
	f.StringVar(&o.dest, "dest", "", "local mirror folder whose state and copies prove each file; never created (required unless --ignore-state)")
	f.StringVar(&o.stateDir, "state-dir", "", "folder for the state file of --dest (default: the per-user config folder's seestar-copy)")
	f.BoolVar(&o.trustState, "trust-state", false, "prove a file by its state record alone, without reading the local copy")
	f.BoolVar(&o.ignoreState, "ignore-state", false, "read no state, and delete whatever is selected, copied or not; needs --include")
	f.Var((*ageValue)(&o.olderThan), "older-than", "delete only files whose device time is at least this old; 0 for no limit")
	o.selection.register(f)
	f.BoolVar(&o.force, "force", false, "delete; without it delete only previews")
	f.BoolVar(&o.dryRun, "dry-run", false, "only report what would be deleted, and change nothing; the default without --force")
	return cmd
}

// validate checks the flags without touching the network or the disk.
func (o *deleteOptions) validate() error {
	if o.endpoint == "" {
		return &usageError{Option: "--endpoint", Rule: "required; give the SMB host:port"}
	}
	if err := validateEndpoint(o.endpoint); err != nil {
		return err
	}
	sel, err := o.selection.parse()
	if err != nil {
		return err
	}
	o.selector = sel
	if o.dryRun && o.force {
		return &usageError{Option: "--force", Rule: "give --force or --dry-run, not both"}
	}
	if o.trustState && o.ignoreState {
		return &usageError{Option: "--trust-state", Rule: "give --trust-state or --ignore-state, not both"}
	}
	if o.ignoreState {
		switch {
		case len(o.selection.includes) == 0:
			return &usageError{Option: "--ignore-state", Rule: "give at least one --include; --ignore-state deletes whatever is selected"}
		case o.destSet:
			return &usageError{Option: "--dest", Rule: "--ignore-state reads no state; omit it"}
		case o.stateDirSet:
			return &usageError{Option: "--state-dir", Rule: "--ignore-state reads no state; omit it"}
		}
	} else if o.dest == "" {
		return &usageError{Option: "--dest", Rule: "required; give the local mirror folder, or --ignore-state"}
	}
	if o.stateDirSet && o.stateDir == "" {
		return &usageError{Option: "--state-dir", Rule: "must not be empty; omit it for the default"}
	}
	if o.olderThan < 0 {
		return &usageError{Option: "--older-than", Rule: "must not be negative"}
	}
	if !o.force {
		o.dryRun = true
	}
	return nil
}

// dialRemover opens the session a real run deletes through. Only delete
// dials one; tests replace it with a fake over a local folder.
var dialRemover = func(ctx context.Context, endpoint string) (remote.Remover, error) {
	r, err := remote.DialRemover(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// errNoDest is the error for a missing --dest when the copies prove the
// files.
var errNoDest = errors.New("destination not found; give --trust-state if the copies were moved or the drive is offline")

// runDelete loads the state, lists the share and deletes what is proven,
// or in a dry run (a preview) reports what it would delete. A dry run turns the read-only guard on first and dials a session that
// cannot remove anything. A stop by ctx returns nil.
func runDelete(ctx context.Context, o deleteOptions, log *slog.Logger) error {
	if o.dryRun {
		// Before anything else: from here on, any function that changes
		// the local filesystem or the device panics.
		readonly.Enable()
	}
	d := &deleter{opts: o, log: log}
	if !o.ignoreState {
		stateDir, err := resolveStateDir(o.stateDir, o.stateDirSet)
		if err != nil {
			return err
		}
		if d.records, err = state.Load(stateDir, o.dest, log); err != nil {
			return err
		}
		if o.proof() == proofHash {
			root, err := os.OpenRoot(o.dest)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				return errNoDest
			case err != nil:
				return fmt.Errorf("open destination: %w", err)
			}
			defer root.Close()
			d.dest = root
		}
	}
	log.Info("deletion starting", "proof", o.proof(), "older_than", (*ageValue)(&o.olderThan).String(), "dry_run", o.dryRun)
	failed, err := d.run(ctx)
	switch {
	case ctx.Err() != nil:
		return nil
	case err != nil:
		return err
	}
	if o.dryRun {
		log.Info("preview only; add --force to delete")
	}
	switch {
	case failed > 0:
		return fmt.Errorf("failed files: %d", failed)
	}
	return nil
}

// deleter is one delete run.
type deleter struct {
	opts deleteOptions
	log  *slog.Logger
	// records is the loaded state; nil with --ignore-state.
	records map[string]state.Record
	// dest is the destination, open read-only; nil unless the copies
	// prove the files.
	dest *os.Root
	// rm removes files; nil in a dry run.
	rm remote.Remover

	deleted, folders, kept, recent, failed int
	bytes                                  int64
}

// run lists the share and goes through it in reverse, so that it reaches a
// folder after everything in it, then logs a summary. It returns how many
// files or folders failed locally; an error ends the run.
func (d *deleter) run(ctx context.Context) (failed int, err error) {
	start := time.Now()
	var left leftOut
	defer func() {
		names := []string{"deleted", "bytes", "folders"}
		msg := "deletion"
		if d.opts.dryRun {
			names = []string{"delete", "bytes", "remove_folders"}
			msg = "dry run"
		}
		args := []any{names[0], d.deleted, names[1], d.bytes, names[2], d.folders, "kept", d.kept, "recent", d.recent,
			"failed", d.failed, "skipped", len(left.skipped), "unmatched", left.unmatched, "elapsed", time.Since(start).Round(time.Millisecond)}
		switch {
		case ctx.Err() != nil:
			d.log.Info(msg+" stopped", args...)
		case err != nil:
			d.log.Error(msg+" failed", append(args, "error", err)...)
		default:
			d.log.Info(msg+" done", args...)
		}
	}()
	var sess remote.Session
	if d.opts.dryRun {
		sess, err = dial(ctx, d.opts.endpoint)
	} else {
		d.rm, err = dialRemover(ctx, d.opts.endpoint)
		sess = d.rm
	}
	if err != nil {
		return 0, fmt.Errorf("connect to %s: %w", d.opts.endpoint, err)
	}
	defer sess.Close()
	entries, left, err := d.opts.selector.walk(ctx, sess)
	skip := "skipped"
	if d.opts.dryRun {
		skip = "would skip"
	}
	for _, x := range left.skipped {
		d.log.Info(skip, "path", x.path, "exclude", x.by.flag)
	}
	if err != nil {
		return 0, fmt.Errorf("list the share: %w", err)
	}
	in := plan.DeleteInput{Now: time.Now(), OlderThan: d.opts.olderThan, ValidPath: remote.ValidPath}
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
	folders := plan.NewFolders(in.Listing, left.keptParents)
	for i := len(entries) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return d.failed, nil
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
	switch dec.Kind {
	case plan.Recent:
		d.recent++
		return false, nil
	case plan.Keep:
		d.keep(e.Path, dec.Reason, dec.Warn)
		return false, nil
	}
	if d.dest != nil {
		reason, warn, err := proveFile(ctx, d.dest, e.Path, d.records[e.Path])
		switch {
		case ctx.Err() != nil:
			return false, nil
		case err != nil:
			// A local failure fails the file, and the run goes on.
			d.failed++
			d.log.Warn("file failed", "path", e.Path, "reason", err)
			return false, nil
		case reason != "":
			d.keep(e.Path, reason, warn)
			return false, nil
		}
	}
	if d.opts.dryRun {
		d.deleted++
		d.bytes += e.Size
		d.log.Info("would delete", "path", e.Path, "size", e.Size)
		return true, nil
	}
	err := deleteFile(ctx, d.rm, e)
	switch {
	case err == nil:
		d.deleted++
		d.bytes += e.Size
		d.log.Info("file deleted", "path", e.Path, "size", e.Size)
		return true, nil
	case ctx.Err() != nil:
		return false, nil
	case errors.Is(err, remote.ErrChanged):
		d.keep(e.Path, "changed on the device during this run", true)
		return false, nil
	case errors.Is(err, fs.ErrNotExist):
		d.keep(e.Path, "no longer on the device", false)
		return false, nil
	}
	d.failed++
	d.log.Warn("file failed", "path", e.Path, "reason", err)
	return false, err
}

func (d *deleter) keep(p, reason string, warn bool) {
	d.kept++
	level := slog.LevelInfo
	if warn {
		level = slog.LevelWarn
	}
	d.log.Log(context.Background(), level, "file kept", "path", p, "reason", reason)
}

// folder removes a folder the run emptied, or in a dry run reports that it
// would. It reports whether the folder is gone, or would be. An error is a
// remote failure, which ends the run.
func (d *deleter) folder(ctx context.Context, p string) (bool, error) {
	if d.opts.dryRun {
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
// read. It only reads.
func proveFile(ctx context.Context, dest *os.Root, p string, rec state.Record) (reason string, warn bool, err error) {
	const missing = "local copy missing; give --trust-state if it was moved"
	name := filepath.FromSlash(p)
	fi, err := dest.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return missing, false, nil
	case err != nil:
		return "", false, err
	case !fi.Mode().IsRegular():
		return "local copy is not a regular file", true, nil
	case fi.Size() != rec.Size:
		return "local copy size differs from the state", true, nil
	}
	f, err := dest.Open(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return missing, false, nil
	case err != nil:
		return "", false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx, f}); err != nil {
		return "", false, err
	}
	if hex.EncodeToString(h.Sum(nil)) != rec.SHA256 {
		return "local copy's SHA-256 differs from the state", true, nil
	}
	return "", false, nil
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
