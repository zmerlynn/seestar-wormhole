package main

import (
	"context"
	"errors"
	"io"
	"slices"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/cli"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// fileCommandNames are the commands mounted from internal/copy/cli.
var fileCommandNames = []string{"sync", "find", "browse", "delete"}

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
		log.Error(o.file.Name()+" failed", "error", err)
	}
	return err
}
