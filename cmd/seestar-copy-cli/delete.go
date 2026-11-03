package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/remote"
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
	selector engine.Selector
	// dryRun is set by --dry-run, and by validate when --force is not
	// given: either way the run only previews.
	dryRun bool
	force  bool
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
A file the telescope rewrites between delete's check and its removal is
lost, so avoid running delete while the telescope is busy.
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

// runDelete resolves the state folder and runs the deletion. A dry run
// turns the read-only guard on first.
func runDelete(ctx context.Context, o deleteOptions, log *slog.Logger) error {
	if o.dryRun {
		// Before anything else: from here on, any function that changes
		// the local filesystem or the device panics.
		readonly.Enable()
	}
	var stateDir string
	if !o.ignoreState {
		var err error
		if stateDir, err = resolveStateDir(o.stateDir, o.stateDirSet); err != nil {
			return err
		}
	}
	_, err := engine.RunDelete(ctx, engine.DeleteOptions{
		Endpoint: o.endpoint, Dest: o.dest, StateDir: stateDir, TrustState: o.trustState, IgnoreState: o.ignoreState,
		OlderThan: o.olderThan, Selector: o.selector, DryRun: o.dryRun, Dial: dial, OpenRemover: dialRemover,
	}, log)
	return err
}
