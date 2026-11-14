package cli

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/engine"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// maxWorkers bounds --workers; every worker shares the one SMB session.
const maxWorkers = 64

type syncOptions struct {
	profile  profileFlag
	endpoint string
	// follow follows a saved telescope by its serial at the endpoint the
	// profile filled.
	follow    *follow
	scan      scanFlag
	dest      string
	selection selectionFlags
	// selector is set by validate from selection.
	selector      engine.Selector
	interval      time.Duration
	intervalSet   bool
	once          bool
	dryRun        bool
	readyAge      time.Duration
	firstPassAge  time.Duration
	workers       int
	historyDir    string
	historyDirSet bool
}

// NewSync returns the sync command of program.
func NewSync(program string, load LoadProfile) *Command {
	var o syncOptions
	c := &Command{}
	c.Command = &cobra.Command{
		Use:   "sync --endpoint HOST:PORT --dest DIR",
		Short: "Mirror the telescope's images into a local folder",
		Long: `Mirror the telescope's images into a local folder.
Each pass lists the whole share, except what an --exclude pattern matches,
copies files that are new or changed on the device, and records them in a
copy history under --history-dir, so a file moved out of --dest is not copied
again. Passes repeat every --interval until Ctrl+C, or run once with --once.
Each pass holds the lock file .seestar-wormhole.lock in --dest, which stays
there: if another sync or a delete holds it, the first pass exits 1 and a
later pass is skipped. A top-level share entry of that name is not copied.
The telescope is never written to. --dry-run runs one pass that only logs
what it would copy, replace, adopt or skip, and changes nothing on disk.
Patterns use .gitignore syntax, but case is ignored and a leading ! is a
usage error. An --exclude pattern with no / matches a file or folder name
at any depth; one with a / at the start or in the middle is a path from the
share root; a trailing / matches folders only; ** matches any number of
folders. A matched folder is skipped with everything in it. Unless --no-default-excludes is given, sync also
skips what these flags would:
` + defaultExcludeFlags() + `
With --include, sync copies only files that an --include pattern matches, or
that are in a folder one matches; --include '/MyWorks' copies that folder and
--include '*.jpg' JPEG files anywhere. Excludes always win, whatever the order:
--include x --exclude x selects nothing, and no include brings back what an
exclude skips.
--profile default fills each flag not given from the app's saved settings:
--dest, --history-dir, --exclude, --include, --no-default-excludes,
--interval, --once and --workers from its Copy settings, and --endpoint
from its connection. A flag given always wins, and one --exclude or
--include replaces the whole saved list. Without --profile, no saved
settings are read.
` + followHelp + `Durations use Go units, such as 30s, 5m or 1h30m.
Logs and errors go to stderr; help goes to stdout.
Exit 0 when stopped or after a clean --once or --dry-run pass, 1 on a runtime
failure or a --once or --dry-run pass with failed files, 2 on a usage error.`,
		Example: "  " + program + " sync --endpoint 127.0.0.1:4445 --dest ~/Seestar\n" +
			"  " + program + " sync --profile default",
		Args: noArguments,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.profile.fill(cmd.Flags(), load, o.fillSaved); err != nil {
				return err
			}
			if err := o.scan.apply(cmd.Flags(), o.follow); err != nil {
				return err
			}
			o.historyDirSet = o.profile.present(cmd.Flags(), "history-dir")
			o.intervalSet = cmd.Flags().Changed("interval")
			if err := o.validate(); err != nil {
				return err
			}
			c.store(o.dryRun, func(ctx context.Context, _ io.Writer, log *slog.Logger) error {
				o.profile.log(log)
				return runSync(ctx, o, log)
			})
			return nil
		},
	}
	f := c.Flags()
	o.profile.register(f)
	o.scan.register(f)
	f.StringVar(&o.endpoint, "endpoint", "", "SMB host:port of the telescope or a proxy (required)")
	f.StringVar(&o.dest, "dest", "", "local mirror folder, created if missing (required)")
	o.selection.register(f)
	f.DurationVar(&o.interval, "interval", 5*time.Minute, "wait between the end of one pass and the start of the next")
	f.BoolVar(&o.once, "once", false, "run one pass and exit")
	f.BoolVar(&o.dryRun, "dry-run", false, "run one pass that only reports what it would do, and change nothing on disk")
	f.DurationVar(&o.readyAge, "ready-age", 2*time.Minute, "copy a file only once its device time is at least this old")
	f.DurationVar(&o.firstPassAge, "first-pass-age", 10*time.Minute, "on the first pass, files at least this old count as finished")
	f.IntVar(&o.workers, "workers", 4, "files copied at once (1-64)")
	f.StringVar(&o.historyDir, "history-dir", "", "folder for the copy history (default: copy-history in the per-user config folder's seestar-wormhole)")
	return c
}

// validate checks the flags without touching the network or the disk.
func (o *syncOptions) validate() error {
	if o.endpoint == "" {
		return &diagnostic.OptionError{Option: "--endpoint", Rule: "required; give the SMB host:port"}
	}
	if err := validateEndpoint(o.endpoint); err != nil {
		return err
	}
	if o.dest == "" {
		return &diagnostic.OptionError{Option: "--dest", Rule: "required; give the local mirror folder"}
	}
	sel, err := o.selection.parse()
	if err != nil {
		return err
	}
	o.selector = sel
	if o.interval <= 0 {
		return &diagnostic.OptionError{Option: "--interval", Rule: "must be more than zero"}
	}
	if o.dryRun && o.intervalSet {
		return &diagnostic.OptionError{Option: "--dry-run", Rule: "runs one pass; do not give --interval with it"}
	}
	if o.readyAge < 0 {
		return &diagnostic.OptionError{Option: "--ready-age", Rule: "must not be negative"}
	}
	if o.firstPassAge < 0 {
		return &diagnostic.OptionError{Option: "--first-pass-age", Rule: "must not be negative"}
	}
	if o.workers < 1 || o.workers > maxWorkers {
		return &diagnostic.OptionError{Option: "--workers", Rule: "must be 1 to 64"}
	}
	if o.historyDirSet && o.historyDir == "" {
		return &diagnostic.OptionError{Option: "--history-dir", Rule: "must not be empty; omit it for the default"}
	}
	return nil
}

func validateEndpoint(endpoint string) error {
	const rule = "use host:port, such as 127.0.0.1:4445"
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" {
		return &diagnostic.OptionError{Option: "--endpoint", Rule: rule}
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return &diagnostic.OptionError{Option: "--endpoint", Rule: rule}
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

// dial opens a read-only session, for each sync pass, find, browse and a
// delete preview, whose callbacks log to log. Tests replace it with the
// io/fs fake.
var dial = func(ctx context.Context, endpoint string, log *slog.Logger) (remote.Session, error) {
	return remote.Dial(ctx, endpoint, log)
}

// historyDir resolves the history folder: a run that writes moves the old
// default folder first, and a read-only run reads it in place.
func historyDir(explicit string, readOnly bool, log *slog.Logger) (string, error) {
	if readOnly {
		return state.ReadHistoryDir(explicit)
	}
	return state.HistoryDir(explicit, log)
}

// runSync names the destination, resolves the history folder and runs the
// sync. A dry run is read-only: the program turns the guard on before it
// calls this.
func runSync(ctx context.Context, o syncOptions, log *slog.Logger) error {
	log.Info("sync starting", "dest", o.dest, "dry_run", o.dryRun)
	stateDir, err := historyDir(o.historyDir, o.dryRun, log)
	if err != nil {
		return err
	}
	return engine.RunSync(ctx, engine.SyncOptions{
		Endpoint: o.endpoint, Dest: o.dest, StateDir: stateDir, Selector: o.selector,
		Interval: o.interval, Once: o.once, DryRun: o.dryRun,
		ReadyAge: o.readyAge, FirstPassAge: o.firstPassAge, Workers: o.workers,
		// When o.follow is set, the dial may reach the telescope at
		// another address.
		Dial: func(ctx context.Context, _ string) (remote.Session, error) {
			return connect(ctx, o.follow, o.endpoint, log, dial)
		},
	}, log)
}
