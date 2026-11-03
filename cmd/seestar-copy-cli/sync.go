package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// maxWorkers bounds --workers; every worker shares the one SMB session.
const maxWorkers = 64

type syncOptions struct {
	endpoint  string
	dest      string
	selection selectionFlags
	// selector is set by validate from selection.
	selector     engine.Selector
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
With --include, sync copies only files that an --include pattern matches, or
that are in a folder one matches; --include '/MyWorks' copies that folder and
--include '*.jpg' JPEG files anywhere. Excludes always win, whatever the order:
--include x --exclude x selects nothing, and no include brings back what an
exclude skips.
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
	o.selection.register(f)
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
	sel, err := o.selection.parse()
	if err != nil {
		return err
	}
	o.selector = sel
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
	for _, p := range engine.DefaultExcludes {
		b.WriteString("  --exclude '" + p + "'\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// resolveStateDir returns --state-dir if it was given, or the per-user
// config folder's seestar-copy, as seestar-wormhole chooses its own.
func resolveStateDir(stateDir string, set bool) (string, error) {
	if set {
		return stateDir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("no per-user config folder; give --state-dir")
	}
	return filepath.Join(base, "seestar-copy"), nil
}

// dial opens a read-only session, for each sync pass, find, browse and a
// delete preview. Tests replace it with the io/fs fake.
var dial = func(ctx context.Context, endpoint string) (remote.Session, error) {
	return remote.Dial(ctx, endpoint)
}

// runSync resolves the state folder and runs the sync. A dry run turns
// the read-only guard on first.
func runSync(ctx context.Context, o syncOptions, log *slog.Logger) error {
	if o.dryRun {
		// Before anything touches the disk: from here on, any function
		// that changes the local filesystem panics.
		readonly.Enable()
	}
	stateDir, err := resolveStateDir(o.stateDir, o.stateDirSet)
	if err != nil {
		return err
	}
	return engine.RunSync(ctx, engine.SyncOptions{
		Endpoint: o.endpoint, Dest: o.dest, StateDir: stateDir, Selector: o.selector,
		Interval: o.interval, Once: o.once, DryRun: o.dryRun,
		ReadyAge: o.readyAge, FirstPassAge: o.firstPassAge, Workers: o.workers, Dial: dial,
	}, log)
}
