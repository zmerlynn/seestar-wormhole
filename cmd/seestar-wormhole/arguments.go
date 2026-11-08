package main

import (
	"errors"
	"io"
	"log/slog"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/cli"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// argumentError contains only registered option names and fixed rules. Supplied
// values and the parser's formatted errors never cross this boundary.
type argumentError = diagnostic.OptionError

// flagParseError maps a parse error of cmd to an argumentError. cmd is nil
// for errors that come from outside a flag set.
func flagParseError(cmd *cobra.Command, err error) error {
	var detail *argumentError
	if errors.As(err, &detail) {
		return detail
	}
	var unknown *pflag.NotExistError
	if errors.As(err, &unknown) {
		return unknownFlagError(cmd, unknown)
	}
	var missing *pflag.ValueRequiredError
	if errors.As(err, &missing) {
		return &argumentError{Option: "--" + missing.GetFlag().Name, Rule: "requires a value"}
	}
	var invalid *pflag.InvalidValueError
	if errors.As(err, &invalid) {
		rule := "use a supported value"
		if cmd != nil && fileCommand(cmd.Name()) {
			if r, ok := cli.ValueRule(invalid.GetFlag().Value.Type()); ok {
				rule = r
			}
			return &argumentError{Option: "--" + invalid.GetFlag().Name, Rule: rule}
		}
		switch invalid.GetFlag().Value.Type() {
		case "duration":
			rule = "use a duration such as 2m or 0"
		case "int":
			rule = "use an integer byte count"
		case "bool":
			rule = "use a boolean value"
		}
		return &argumentError{Option: "--" + invalid.GetFlag().Name, Rule: rule}
	}
	return &argumentError{Option: "arguments", Rule: "use only supported options"}
}

// retiredFlags holds, per command, the flags it no longer takes and the rule
// that says what replaced each one.
var retiredFlags = map[string]map[string]string{
	// 0.1.0's copy CLI had --source, and the file commands took --state-dir
	// until they joined this program, whose proxy --state-dir means another
	// folder.
	"sync":   {"--source": "removed; sync copies the whole share, so leave folders out with --exclude", "--state-dir": historyDirRenamed},
	"find":   {"--state-dir": historyDirRenamed},
	"browse": {"--state-dir": historyDirRenamed},
	"delete": {"--state-dir": historyDirRenamed},
}

// historyDirRenamed is the rule for a file command's old --state-dir.
const historyDirRenamed = "renamed; use --history-dir for the copy history folder"

// flagHomeOrder lists the commands searched first for an unknown flag's home;
// the rest of the tree follows in its own order.
var flagHomeOrder = []string{"sync", "find", "browse", "delete", "proxy"}

// unknownFlagError names a flag only when this program defines that name
// somewhere in its tree, or once did. Any other name may be a typed secret,
// so it is reported as arguments.
func unknownFlagError(cmd *cobra.Command, e *pflag.NotExistError) error {
	name, short := e.GetSpecifiedName(), e.GetSpecifiedShortnames() != ""
	option := "--" + name
	if short {
		option = "-" + name
	}
	unknown := &argumentError{Option: "arguments", Rule: "unknown flag"}
	if cmd == nil {
		return unknown
	}
	if rule, ok := retiredFlags[cmd.Name()][option]; ok {
		return &argumentError{Option: option, Rule: rule}
	}
	if option == "--version" {
		return &argumentError{Option: option, Rule: "not a flag of " + cmd.Name() + "; use " + programName + " --version"}
	}
	if home := flagHome(cmd.Root(), cmd, name, short); home != nil {
		return &argumentError{Option: option, Rule: "not a flag of " + cmd.Name() + "; see " + home.CommandPath() + " --help"}
	}
	return unknown
}

// flagHome returns the first command other than cmd that defines the flag.
func flagHome(root, cmd *cobra.Command, name string, short bool) *cobra.Command {
	var order []*cobra.Command
	for _, n := range flagHomeOrder {
		if c, _, err := root.Find([]string{n}); err == nil && c != root {
			order = append(order, c)
		}
	}
	order = append(order, root.Commands()...)
	for _, c := range append(order, root) {
		if c == cmd {
			continue
		}
		if short && len(name) == 1 && c.Flags().ShorthandLookup(name) != nil || !short && c.Flags().Lookup(name) != nil {
			return c
		}
	}
	return nil
}

// configurationLogger projects only the typed setting and fixed rule. It is
// shared by parser and runtime configuration failures, regardless of log options.
func configurationLogger(out io.Writer, command string, detail *argumentError) *slog.Logger {
	return newLogger("json", out).With("option", detail.Option, "rule", detail.Rule,
		"help", programName+" "+command+" --help")
}

func logConfigurationError(out io.Writer, command string, err error) {
	var detail *argumentError
	if errors.As(err, &detail) {
		logger := configurationLogger(out, command, detail)
		if fileCommand(command) {
			// A file command's default format is text, and the record keeps it
			// whatever --log-format says, since that may be the flag at fault.
			logger = newLogger("text", out).With("option", detail.Option, "rule", detail.Rule,
				"help", programName+" "+command+" --help")
		}
		if command == "proxy" || command == "discovery" {
			logger.Error(command+" failed", "phase", "arguments", "reason", "invalid_option", "cleanup_reason", "none")
		} else {
			logger.Error(command+" failed", "phase", "arguments", "reason", "invalid_option")
		}
	}
}

// logRootError logs a usage error of the root command under the program name.
func logRootError(out io.Writer, err error) {
	var detail *argumentError
	if errors.As(err, &detail) {
		newLogger("json", out).Error(programName+" failed", "phase", "arguments", "reason", "invalid_option",
			"option", detail.Option, "rule", detail.Rule, "help", programName+" --help")
	}
}
