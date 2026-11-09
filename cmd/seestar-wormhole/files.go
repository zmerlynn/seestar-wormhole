package main

import (
	"context"
	"errors"
	"io"
	"slices"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/cli"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// copyCommandNames are the file commands that reach a telescope's share,
// take --profile and once took --state-dir.
var copyCommandNames = []string{"sync", "find", "browse", "delete"}

// fileCommandNames are the commands mounted from internal/copy/cli: the
// copy commands and scan.
var fileCommandNames = append(slices.Clone(copyCommandNames), "scan")

// fileSources are the diagnostic sources of the file commands, named on
// their failure records.
var fileSources = map[string]string{
	"sync": diagnostic.SourceCopy, "find": diagnostic.SourceFind,
	"browse": diagnostic.SourceBrowse, "delete": diagnostic.SourceDelete,
	"scan": diagnostic.SourceScan,
}

func fileCommand(name string) bool { return slices.Contains(fileCommandNames, name) }

// addFileCommands mounts the file commands. Each has its own logging
// options, whose default format is text, and no --version.
func addFileCommands(root *cobra.Command, o *options) {
	for _, c := range cli.Commands(programName, loadDefaultProfile) {
		var logs logOptions
		registerLoggingOptions(c.Flags(), &logs, "text")
		c.PreRunE = func(*cobra.Command, []string) error {
			logs.use(o)
			o.file = c
			return validateLoggingOptions(o)
		}
		root.AddCommand(c.Command)
	}
}

// enableReadOnly turns the read-only guard on: from then on, any function
// that changes the local filesystem or the device panics. Tests replace it
// with a recorder.
var enableReadOnly = readonly.Enable

// runFiles runs the file command that validated, logging to logs in its own
// format and level. A read-only one runs with the read-only guard on.
func runFiles(ctx context.Context, o options, out, logs io.Writer) error {
	level, _ := diagnostic.Level(o.level)
	log := diagnostic.Logger(o.format, logs, level)
	if o.file.ReadOnly {
		enableReadOnly()
	}
	err := o.file.Run(ctx, out, log)
	if err == nil {
		return nil
	}
	if detail, ok := errors.AsType[*argumentError](err); ok {
		logConfigurationError(logs, o.file.Name(), detail)
	} else {
		// The keys read a lock held elsewhere as busy, as the app's trail
		// does; error keeps the original text and the returned error is
		// unchanged.
		name := o.file.Name()
		log.Error(name+" failed", append(diagnostic.OpKeys(fileSources[name], diagnostic.OpRun, state.Classified(err)), "error", err)...)
	}
	return err
}
