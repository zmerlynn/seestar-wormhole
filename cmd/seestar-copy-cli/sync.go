package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-copy/internal/copier"
	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// maxWorkers bounds --workers; every worker shares the one SMB session.
const maxWorkers = 64

type syncOptions struct {
	endpoint          string
	dest              string
	excludeFlags      []string
	noDefaultExcludes bool
	// excludes is set by validate from the two flags above.
	excludes     []exclude
	interval     time.Duration
	intervalSet  bool
	once         bool
	dryRun       bool
	readyAge     time.Duration
	firstPassAge time.Duration
	workers      int
	stateDir     string
	stateDirSet  bool
}

func newSyncCommand(log *slog.Logger) *cobra.Command {
	var o syncOptions
	cmd := &cobra.Command{
		Use:   "sync --endpoint HOST:PORT --dest DIR",
		Short: "Mirror the telescope's images into a local folder",
		Long: `Mirror the telescope's images into a local folder.
Each pass lists the whole share, except what an --exclude pattern matches,
copies files that are new or changed on the device, and records them in a
state file under --state-dir, so a file moved out of --dest is not copied
again. Passes repeat every --interval until Ctrl+C, or run once with --once.
The telescope is never written to. --dry-run runs one pass that only logs
what it would copy, replace, adopt or skip, and changes nothing on disk.
An --exclude pattern with no / matches a file or folder name at any depth;
one with a / is a path from the share root, and a leading / is optional.
* and ? match within one name; case is ignored. A matched folder is skipped
with everything in it. Unless --no-default-excludes is given, sync also
skips what these flags would:
` + defaultExcludeFlags() + `
Durations use Go units, such as 30s, 5m or 1h30m.
Logs and errors go to stderr; help goes to stdout.
Exit 0 when stopped or after a clean --once or --dry-run pass, 1 on a runtime
failure or a --once or --dry-run pass with failed files, 2 on a usage error.`,
		Example: "  " + programName + " sync --endpoint 127.0.0.1:4445 --dest ~/Seestar",
		Args:    noArguments,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.stateDirSet = cmd.Flags().Changed("state-dir")
			o.intervalSet = cmd.Flags().Changed("interval")
			if err := o.validate(); err != nil {
				return err
			}
			return runSync(cmd.Context(), o, log)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.endpoint, "endpoint", "", "SMB host:port of the telescope or a proxy (required)")
	f.StringVar(&o.dest, "dest", "", "local mirror folder, created if missing (required)")
	f.StringArrayVar(&o.excludeFlags, "exclude", nil, "skip files and folders that match this pattern, as well as the built-in ones; repeatable")
	f.BoolVar(&o.noDefaultExcludes, "no-default-excludes", false, "do not skip what the built-in patterns match")
	f.DurationVar(&o.interval, "interval", 5*time.Minute, "wait between the end of one pass and the start of the next")
	f.BoolVar(&o.once, "once", false, "run one pass and exit")
	f.BoolVar(&o.dryRun, "dry-run", false, "run one pass that only reports what it would do, and change nothing on disk")
	f.DurationVar(&o.readyAge, "ready-age", 2*time.Minute, "copy a file only once its device time is at least this old")
	f.DurationVar(&o.firstPassAge, "first-pass-age", 10*time.Minute, "on the first pass, files at least this old count as finished")
	f.IntVar(&o.workers, "workers", 4, "files copied at once (1-64)")
	f.StringVar(&o.stateDir, "state-dir", "", "folder for the state file (default: the per-user config folder's seestar-copy)")
	return cmd
}

// validate checks the flags without touching the network or the disk.
func (o *syncOptions) validate() error {
	if o.endpoint == "" {
		return &usageError{Option: "--endpoint", Rule: "required; give the SMB host:port"}
	}
	if err := validateEndpoint(o.endpoint); err != nil {
		return err
	}
	if o.dest == "" {
		return &usageError{Option: "--dest", Rule: "required; give the local mirror folder"}
	}
	excludes, err := parseExcludes(o.excludeFlags, o.noDefaultExcludes)
	if err != nil {
		return err
	}
	o.excludes = excludes
	if o.interval <= 0 {
		return &usageError{Option: "--interval", Rule: "must be more than zero"}
	}
	if o.dryRun && o.intervalSet {
		return &usageError{Option: "--dry-run", Rule: "runs one pass; do not give --interval with it"}
	}
	if o.readyAge < 0 {
		return &usageError{Option: "--ready-age", Rule: "must not be negative"}
	}
	if o.firstPassAge < 0 {
		return &usageError{Option: "--first-pass-age", Rule: "must not be negative"}
	}
	if o.workers < 1 || o.workers > maxWorkers {
		return &usageError{Option: "--workers", Rule: "must be 1 to 64"}
	}
	if o.stateDirSet && o.stateDir == "" {
		return &usageError{Option: "--state-dir", Rule: "must not be empty; omit it for the default"}
	}
	return nil
}

func validateEndpoint(endpoint string) error {
	const rule = "use host:port, such as 127.0.0.1:4445"
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" {
		return &usageError{Option: "--endpoint", Rule: rule}
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return &usageError{Option: "--endpoint", Rule: rule}
	}
	return nil
}

// defaultExcludeFlags renders the built-in patterns as the --exclude flags
// they equal, one per line, quoted for a shell.
func defaultExcludeFlags() string {
	var b strings.Builder
	for _, p := range defaultExcludes {
		b.WriteString("  --exclude '" + p + "'\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// resolveStateDir returns --state-dir, or the per-user config folder's seestar-copy, as
// seestar-wormhole chooses its own.
func (o *syncOptions) resolveStateDir() (string, error) {
	if o.stateDirSet {
		return o.stateDir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("no per-user config folder; give --state-dir")
	}
	return filepath.Join(base, "seestar-copy"), nil
}

// dial opens the per-pass session. Tests replace it with the io/fs fake.
var dial = func(ctx context.Context, endpoint string) (remote.Session, error) {
	return remote.Dial(ctx, endpoint)
}

// runSync opens the state and the destination, then runs passes until ctx is
// done, or runs one with --once. A stop by ctx returns nil. A dry run is
// [runDry].
func runSync(ctx context.Context, o syncOptions, log *slog.Logger) error {
	if o.dryRun {
		// Before anything touches the disk: from here on, any function
		// that changes the local filesystem panics.
		readonly.Enable()
		return runDry(ctx, o, log)
	}
	stateDir, err := o.resolveStateDir()
	if err != nil {
		return err
	}
	st, err := state.Open(stateDir, o.dest, log)
	if err != nil {
		return err
	}
	defer st.Close()
	cp, err := copier.New(o.dest, st, log)
	if err != nil {
		return err
	}
	defer cp.Close()
	s := &syncer{opts: o, log: log, records: st.Snapshot, stat: cp.Stat, copier: cp, firstPass: true}
	for {
		failed, err := s.pass(ctx)
		switch {
		case ctx.Err() != nil:
			return nil
		case o.once && err != nil:
			return err
		case o.once && failed > 0:
			return fmt.Errorf("failed files: %d", failed)
		case o.once:
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(o.interval):
		}
	}
}

// runDry runs one pass that only reports what a real pass would do. It
// reads the state and the destination, if they exist, through read-only
// views, and never opens [state.State] or [copier.Copier], which can write.
// Failed files make it return an error, as with --once.
func runDry(ctx context.Context, o syncOptions, log *slog.Logger) error {
	stateDir, err := o.resolveStateDir()
	if err != nil {
		return err
	}
	records, err := state.Load(stateDir, o.dest, log)
	if err != nil {
		return err
	}
	dest, err := copier.OpenDest(o.dest)
	if err != nil {
		return err
	}
	defer dest.Close()
	s := &syncer{opts: o, log: log, records: func() map[string]state.Record { return records }, stat: dest.Stat, firstPass: true}
	failed, err := s.dryPass(ctx)
	switch {
	case ctx.Err() != nil:
		return nil
	case err != nil:
		return err
	case failed > 0:
		return fmt.Errorf("failed files: %d", failed)
	}
	return nil
}

// syncer holds what one process's passes share.
type syncer struct {
	opts syncOptions
	log  *slog.Logger
	// records returns the state's records, and stat the local stat of a
	// share path, for the pass decisions.
	records func() map[string]state.Record
	stat    func(string) plan.Local
	// copier carries out the decisions; nil in a dry run.
	copier *copier.Copier
	// previous is the last pass's listing; firstPass is set until a pass
	// has listed the share.
	previous  []plan.Entry
	firstPass bool
}

// pass runs one pass, logs a line for each entry an exclude skipped and its
// summary, and returns how many files failed. An error means the session or
// a listing failed, and nothing was copied.
func (s *syncer) pass(ctx context.Context) (failed int, err error) {
	start := time.Now()
	var copied, replaced, adopted, waiting, skipped int
	var bytes int64
	defer func() {
		args := []any{"copied", copied, "replaced", replaced, "adopted", adopted, "waiting", waiting,
			"failed", failed, "skipped", skipped, "bytes", bytes, "elapsed", time.Since(start).Round(time.Millisecond)}
		switch {
		case ctx.Err() != nil:
			s.log.Info("pass stopped", args...)
		case err != nil:
			s.log.Error("pass failed", append(args, "error", err)...)
		default:
			s.log.Info("pass done", args...)
		}
	}()
	// The session lives as long as ctx, not just this call: a stop then
	// ends a call the server does not answer.
	sess, err := dial(ctx, s.opts.endpoint)
	if err != nil {
		return 0, fmt.Errorf("connect to %s: %w", s.opts.endpoint, err)
	}
	defer sess.Close()
	// A failed listing still logs and counts what it skipped before failing.
	decisions, counts, sel, err := s.decide(ctx, sess)
	skipped = len(sel.skipped)
	for _, x := range sel.skipped {
		s.log.Info("skipped", "path", x.path, "exclude", x.by.flag)
	}
	if err != nil {
		return 0, err
	}
	// Each file is logged as it finishes, not at the end of the pass.
	s.copier.Run(ctx, sess, s.opts.workers, decisions, func(r copier.Result) {
		if r.Err != nil {
			failed++
			// After a stop, every unfinished file fails for that reason.
			if ctx.Err() == nil {
				s.log.Warn("file failed", "path", r.Decision.Path, "reason", r.Err)
			}
			return
		}
		args := []any{"path", r.Record.Path, "size", r.Record.Size}
		switch r.Record.How {
		case state.Copied:
			copied++
			bytes += r.Record.Size
			if r.Decision.Overwrite {
				args = append(args, "overwrote", true)
			}
		case state.Replaced:
			replaced++
			bytes += r.Record.Size
		case state.Adopted:
			adopted++
		}
		s.log.Info("file "+string(r.Record.How), args...)
	})
	failed += counts.Fail
	waiting = counts.Wait
	return failed, nil
}

// dryPass is a pass that only logs what [syncer.pass] would do: a line for
// each file it would copy, replace or adopt, or that is waiting, the same
// line for a failed file, and a summary. It returns how many files would
// fail. An error means the session or a listing failed.
func (s *syncer) dryPass(ctx context.Context) (failed int, err error) {
	start := time.Now()
	var counts plan.Counts
	var sel selection
	var bytes int64
	defer func() {
		args := []any{"copy", counts.Copy, "replace", counts.Replace, "adopt", counts.Adopt, "waiting", counts.Wait,
			"failed", counts.Fail, "skipped", len(sel.skipped), "bytes", bytes, "elapsed", time.Since(start).Round(time.Millisecond)}
		switch {
		case ctx.Err() != nil:
			s.log.Info("dry run stopped", args...)
		case err != nil:
			s.log.Error("dry run failed", append(args, "error", err)...)
		default:
			s.log.Info("dry run done", args...)
		}
	}()
	sess, err := dial(ctx, s.opts.endpoint)
	if err != nil {
		return 0, fmt.Errorf("connect to %s: %w", s.opts.endpoint, err)
	}
	defer sess.Close()
	var decisions []plan.Decision
	decisions, counts, sel, err = s.decide(ctx, sess)
	for _, x := range sel.skipped {
		s.log.Info("would skip", "path", x.path, "exclude", x.by.flag)
	}
	if err != nil {
		return 0, err
	}
	recorded := map[string]bool{}
	for p := range s.records() {
		if top, _, ok := strings.Cut(p, "/"); ok {
			recorded[top] = true
		}
	}
	for _, folder := range sel.folders {
		if !recorded[folder] {
			s.log.Info("no copied files recorded under this top-level folder", "folder", folder)
		}
	}
	for _, d := range decisions {
		args := []any{"path", d.Path, "size", d.Size}
		switch d.Kind {
		case plan.Copy:
			bytes += d.Size
			if d.Overwrite {
				args = append(args, "overwrite", true)
			}
			s.log.Info("would copy", args...)
		case plan.Replace:
			bytes += d.Size
			s.log.Info("would replace", args...)
		case plan.Adopt:
			s.log.Info("would adopt", args...)
		case plan.Wait:
			s.log.Info("would wait", args...)
		}
	}
	return counts.Fail, nil
}

// decide lists the share over sess and returns the pass decisions, their
// counts and what the excludes left out. It logs a line for each file that
// fails. If the listing fails, it returns only what the excludes left out
// before the failure.
func (s *syncer) decide(ctx context.Context, sess remote.Session) ([]plan.Decision, plan.Counts, selection, error) {
	listing, sel, err := s.list(ctx, sess)
	if err != nil {
		return nil, plan.Counts{}, sel, err
	}
	in := plan.Input{
		Listing:      listing,
		FirstPass:    s.firstPass,
		Previous:     s.previous,
		State:        map[string]plan.Recorded{},
		Local:        map[string]plan.Local{},
		Now:          time.Now(),
		ReadyAge:     s.opts.readyAge,
		FirstPassAge: s.opts.firstPassAge,
		Windows:      runtime.GOOS == "windows",
	}
	s.previous, s.firstPass = listing, false
	for p, rec := range s.records() {
		in.State[p] = plan.Recorded{Size: rec.Size, ModTime: rec.MTime}
	}
	for _, p := range plan.NeedLocal(in) {
		in.Local[p] = s.stat(p)
	}
	decisions, counts := plan.Decide(in)
	for _, d := range decisions {
		if d.Kind == plan.Fail {
			s.log.Warn("file failed", "path", d.Path, "reason", d.Reason)
		}
	}
	return decisions, counts, sel, nil
}

// selection is what one listing left out, and its top-level folders.
type selection struct {
	// skipped are the entries an exclude matched, with the first that did,
	// in listing order. A skipped folder's contents are not listed.
	skipped []skippedEntry
	// folders are the listed top-level folders.
	folders []string
}

type skippedEntry struct {
	path string
	by   exclude
}

// list lists the whole share, leaving out what the excludes match. Any
// listing failure, of the root or of a folder that is not excluded, fails
// the pass; the returned selection then holds the entries skipped before it.
func (s *syncer) list(ctx context.Context, sess remote.Session) ([]plan.Entry, selection, error) {
	var sel selection
	entries, err := sess.Walk(ctx, func(p string) bool {
		e, ok := excluded(s.opts.excludes, p)
		if ok {
			sel.skipped = append(sel.skipped, skippedEntry{p, e})
		}
		return ok
	})
	if err != nil {
		return nil, sel, fmt.Errorf("list the share: %w", err)
	}
	listing := make([]plan.Entry, 0, len(entries))
	for _, e := range entries {
		listing = append(listing, plan.Entry{Path: e.Path, Size: e.Size, ModTime: e.ModTime, Regular: e.Kind == remote.Regular})
		if e.Kind == remote.Dir && !strings.Contains(e.Path, "/") {
			sel.folders = append(sel.folders, e.Path)
		}
	}
	return listing, sel, nil
}
