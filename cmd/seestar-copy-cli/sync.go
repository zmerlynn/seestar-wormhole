package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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

var defaultSources = []string{"MyWorks", "Autorun/Dark", "darklibrary"}

type syncOptions struct {
	endpoint     string
	dest         string
	sources      []string
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
Each pass lists every --source on the share, copies files that are new or
changed on the device, and records them in a state file under --state-dir, so
a file moved out of --dest is not copied again. Passes repeat every --interval
until Ctrl+C, or run once with --once. The telescope is never written to.
--dry-run runs one pass that only logs what it would copy, replace or adopt,
and changes nothing on disk.
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
	f.StringArrayVar(&o.sources, "source", defaultSources, "folder on the share to copy; repeatable, and any replaces the defaults")
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
	if err := validateSources(o.sources); err != nil {
		return err
	}
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

// validateSources accepts share-relative folders with forward slashes, and no
// colon, so none can look like a drive. Two sources may not overlap, since
// that would list the same files twice.
func validateSources(sources []string) error {
	const rule = "use a folder relative to the share root, with forward slashes, such as MyWorks"
	for i, s := range sources {
		if s == "." || !fs.ValidPath(s) || strings.ContainsAny(s, `\:`) {
			return &usageError{Option: "--source", Rule: rule}
		}
		for _, other := range sources[:i] {
			if s == other || strings.HasPrefix(s, other+"/") || strings.HasPrefix(other, s+"/") {
				return &usageError{Option: "--source", Rule: "sources must not repeat or contain each other"}
			}
		}
	}
	return nil
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
	// has listed every source.
	previous  []plan.Entry
	firstPass bool
}

// pass runs one pass, logs its summary and returns how many files failed. An
// error means the session or a listing failed, and nothing was copied.
func (s *syncer) pass(ctx context.Context) (failed int, err error) {
	start := time.Now()
	var copied, replaced, adopted, waiting int
	var bytes int64
	defer func() {
		args := []any{"copied", copied, "replaced", replaced, "adopted", adopted, "waiting", waiting,
			"failed", failed, "bytes", bytes, "elapsed", time.Since(start).Round(time.Millisecond)}
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
	decisions, counts, err := s.decide(ctx, sess)
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
	var bytes int64
	defer func() {
		args := []any{"copy", counts.Copy, "replace", counts.Replace, "adopt", counts.Adopt, "waiting", counts.Wait,
			"failed", counts.Fail, "bytes", bytes, "elapsed", time.Since(start).Round(time.Millisecond)}
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
	decisions, counts, err = s.decide(ctx, sess)
	if err != nil {
		return 0, err
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

// decide lists the sources over sess and returns the pass decisions and
// their counts. It logs a line for each file that fails.
func (s *syncer) decide(ctx context.Context, sess remote.Session) ([]plan.Decision, plan.Counts, error) {
	listing, err := s.list(ctx, sess)
	if err != nil {
		return nil, plan.Counts{}, err
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
	return decisions, counts, nil
}

// list lists every source. A missing source is a warning; any other listing
// failure fails the pass.
func (s *syncer) list(ctx context.Context, sess remote.Session) ([]plan.Entry, error) {
	var listing []plan.Entry
	for _, source := range s.opts.sources {
		entries, err := sess.Walk(ctx, source)
		if errors.Is(err, remote.ErrSourceNotFound) {
			s.log.Warn("source not found", "source", source)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", source, err)
		}
		for _, e := range entries {
			listing = append(listing, plan.Entry{Path: e.Path, Size: e.Size, ModTime: e.ModTime, Regular: e.Kind == remote.Regular})
		}
	}
	return listing, nil
}
