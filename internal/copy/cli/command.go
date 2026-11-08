// Package cli holds the file commands sync, find, browse and delete: their
// flags, validation and runs. A program mounts them in its own command tree,
// and that program owns the usage errors, the logs, the exit codes and the
// read-only guard.
package cli

import (
	"context"
	"io"
	"log/slog"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Command is a file command. Executing it only parses and validates its
// flags, nothing touches the network or the disk, and an invalid flag
// comes back as a *diagnostic.OptionError. Once validation passes it sets
// Run and ReadOnly.
type Command struct {
	*cobra.Command
	// Run does the command's work, writing its output to stdout and its
	// logs to log.
	Run func(ctx context.Context, stdout io.Writer, log *slog.Logger) error
	// ReadOnly is true when the run must not change the local filesystem
	// or the device. The program turns the read-only guard on before Run.
	ReadOnly bool
}

func (c *Command) store(readOnly bool, run func(context.Context, io.Writer, *slog.Logger) error) {
	c.ReadOnly, c.Run = readOnly, run
}

// Commands returns the file commands of program, in help order. load reads
// --profile default; nil means the program has none.
func Commands(program string, load LoadProfile) []*Command {
	return []*Command{NewSync(program, load), NewFind(program, load), NewBrowse(program, load), NewDelete(program, load)}
}

// ValueRule returns the rule for a file-command flag value of type typ that
// failed to parse, and whether there is one.
func ValueRule(typ string) (string, bool) {
	switch typ {
	case "duration":
		return "use a duration such as 30s, 5m or 1h", true
	case "age":
		return "use a duration such as 36h, or whole days such as 7d", true
	case "int":
		return "use a whole number", true
	case "bool":
		return "use true or false", true
	}
	return "", false
}

func noArguments(_ *cobra.Command, args []string) error {
	if len(args) != 0 {
		return &diagnostic.OptionError{Option: "arguments", Rule: "positional arguments are not allowed"}
	}
	return nil
}
