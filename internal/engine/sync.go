package engine

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/copier"
	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// SyncOptions are the checked options of a sync.
type SyncOptions struct {
	// Endpoint is the SMB host:port of the telescope or a proxy.
	Endpoint string
	// Dest is the local mirror folder, created if missing.
	Dest string
	// StateDir is the folder for the state file of Dest.
	StateDir string
	Selector Selector
	// Interval is the wait between the end of one pass and the start of
	// the next.
	Interval time.Duration
	// Once runs one pass.
	Once bool
	// DryRun runs one pass that only logs what it would do.
	DryRun       bool
	ReadyAge     time.Duration
	FirstPassAge time.Duration
	// Workers is how many files are copied at once.
	Workers int
	// Dial opens a pass's session.
	Dial func(ctx context.Context, endpoint string) (remote.Session, error)
}

// RunSync opens the state and the destination, then runs passes until ctx is
// done, or runs one with Once. A stop by ctx returns nil. A dry run is
// [dryRun].
func RunSync(ctx context.Context, o SyncOptions, log *slog.Logger) error {
	if o.DryRun {
		return dryRun(ctx, o, log)
	}
	st, err := state.Open(o.StateDir, o.Dest, log)
	if err != nil {
		return err
	}
	defer st.Close()
	cp, err := copier.New(o.Dest, st, log)
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
		case o.Once && err != nil:
			return err
		case o.Once && failed > 0:
			return fmt.Errorf("failed files: %d", failed)
		case o.Once:
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(o.Interval):
		}
	}
}

// dryRun runs one pass that only reports what a real pass would do. It
// reads the state and the destination, if they exist, through read-only
// views, and never opens [state.State] or [copier.Copier], which can write.
// Failed files make it return an error, as with Once.
func dryRun(ctx context.Context, o SyncOptions, log *slog.Logger) error {
	records, err := state.Load(o.StateDir, o.Dest, log)
	if err != nil {
		return err
	}
	dest, err := copier.OpenDest(o.Dest)
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

// syncer holds what the passes of one [RunSync] share.
type syncer struct {
	opts SyncOptions
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

// pass runs one pass, logs a line for each entry an exclude skipped and a
// summary, which also counts what the includes left out, and returns how
// many files failed. An error means the session or a listing failed, and
// nothing was copied.
func (s *syncer) pass(ctx context.Context) (failed int, err error) {
	start := time.Now()
	var copied, replaced, adopted, waiting, skipped, unmatched int
	var bytes int64
	defer func() {
		elapsed := time.Since(start)
		args := []any{"copied", copied, "replaced", replaced, "adopted", adopted, "waiting", waiting,
			"failed", failed, "skipped", skipped, "unmatched", unmatched, "bytes", bytes, "elapsed", elapsed.Round(time.Millisecond)}
		if bytes > 0 && elapsed > 0 {
			args = append(args, "rate", fmt.Sprintf("%.1fMiB/s", float64(bytes)/(1<<20)/elapsed.Seconds()))
		}
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
	sess, err := s.opts.Dial(ctx, s.opts.Endpoint)
	if err != nil {
		return 0, fmt.Errorf("connect to %s: %w", s.opts.Endpoint, err)
	}
	defer sess.Close()
	// A failed listing still logs and counts what it skipped before failing.
	decisions, counts, sel, err := s.decide(ctx, sess)
	skipped, unmatched = len(sel.Skipped), sel.Unmatched
	for _, x := range sel.Skipped {
		s.log.Info("skipped", "path", x.Path, "exclude", x.Exclude)
	}
	if err != nil {
		return 0, err
	}
	// Each file is logged as it finishes, not at the end of the pass.
	s.copier.Run(ctx, sess, s.opts.Workers, decisions, func(r copier.Result) {
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
			args = append(args, "elapsed", r.Elapsed.Round(time.Millisecond))
		case state.Replaced:
			replaced++
			bytes += r.Record.Size
			args = append(args, "elapsed", r.Elapsed.Round(time.Millisecond))
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
			"failed", counts.Fail, "skipped", len(sel.Skipped), "unmatched", sel.Unmatched, "bytes", bytes, "elapsed", time.Since(start).Round(time.Millisecond)}
		switch {
		case ctx.Err() != nil:
			s.log.Info("dry run stopped", args...)
		case err != nil:
			s.log.Error("dry run failed", append(args, "error", err)...)
		default:
			s.log.Info("dry run done", args...)
		}
	}()
	sess, err := s.opts.Dial(ctx, s.opts.Endpoint)
	if err != nil {
		return 0, fmt.Errorf("connect to %s: %w", s.opts.Endpoint, err)
	}
	defer sess.Close()
	var decisions []plan.Decision
	decisions, counts, sel, err = s.decide(ctx, sess)
	for _, x := range sel.Skipped {
		s.log.Info("would skip", "path", x.Path, "exclude", x.Exclude)
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
// counts and what the selection left out. It logs a line for each file that
// fails. If the listing fails, it returns only what the selection left out
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
		ReadyAge:     s.opts.ReadyAge,
		FirstPassAge: s.opts.FirstPassAge,
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
	LeftOut
	// folders are the listed top-level folders.
	folders []string
}

// list lists the whole share, leaving out what the selection does. Any
// listing failure, of the root or of a folder that is kept, fails the pass;
// the returned selection then holds what was left out before it.
func (s *syncer) list(ctx context.Context, sess remote.Session) ([]plan.Entry, selection, error) {
	entries, out, err := s.opts.Selector.Walk(ctx, sess)
	sel := selection{LeftOut: out}
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
