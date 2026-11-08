package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/cli"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/licenses"
)

// usageError names an option and the rule it broke. It maps to exit code 2.
type usageError = diagnostic.OptionError

// newCommand builds the command tree, with the file commands from
// internal/copy/cli. Parsing and validation errors come back as
// *usageError; nothing touches the network or the disk before validation.
func newCommand(stdout io.Writer) (*cobra.Command, []*cli.Command) {
	var showVersion bool
	root := &cobra.Command{
		Use:                programName,
		Short:              "Copy images from a Seestar's SMB share to a local folder",
		SilenceErrors:      true,
		SilenceUsage:       true,
		DisableSuggestions: true,
		CompletionOptions:  cobra.CompletionOptions{DisableDefaultCmd: true},
		Args:               noArguments,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if showVersion {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), versionString())
				return err
			}
			return cmd.Help()
		},
	}
	root.SetOut(stdout)
	root.SetErr(io.Discard)
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		defaultHelp(cmd, args)
		if cmd == root {
			fmt.Fprintln(cmd.OutOrStdout(), "Third-party license information: "+programName+" licenses")
		}
	})
	root.SetHelpCommand(&cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		Long:  "Help prints the help of any command, such as " + programName + " help sync.",
		RunE: func(cmd *cobra.Command, args []string) error {
			topic, rest, err := root.Find(args)
			if err != nil || len(rest) != 0 {
				return &usageError{Option: "help", Rule: "unknown help topic; give a command name, such as sync"}
			}
			topic.InitDefaultHelpFlag() // list -h in the topic's help, as "topic --help" does
			return topic.Help()
		},
	})
	root.SetFlagErrorFunc(flagError)
	root.Flags().BoolVar(&showVersion, "version", false, "print version, revision and platform")
	files := cli.Commands(programName)
	for _, c := range files {
		root.AddCommand(c.Command)
	}
	root.AddCommand(&cobra.Command{
		Use:   "licenses",
		Short: "Print the license of this program and of the libraries in this build",
		Args:  noArguments,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return licenses.Write(cmd.OutOrStdout())
		},
	})
	return root, files
}

func noArguments(_ *cobra.Command, args []string) error {
	if len(args) != 0 {
		return &usageError{Option: "arguments", Rule: "positional arguments are not allowed"}
	}
	return nil
}

// flagError turns the flag parser's errors into a usageError. Flag values are
// never echoed; only the flag name and a fixed rule are reported.
func flagError(cmd *cobra.Command, err error) error {
	if unknown, ok := errors.AsType[*pflag.NotExistError](err); ok {
		name := "--" + unknown.GetSpecifiedName()
		if unknown.GetSpecifiedShortnames() != "" {
			name = "-" + unknown.GetSpecifiedName()
		}
		// 0.1.0 had --source; say what replaced it.
		if name == "--source" && cmd.Name() == "sync" {
			return &usageError{Option: name, Rule: "removed; sync copies the whole share, so leave folders out with --exclude"}
		}
		return &usageError{Option: name, Rule: "unknown flag"}
	}
	if missing, ok := errors.AsType[*pflag.ValueRequiredError](err); ok {
		return &usageError{Option: "--" + missing.GetFlag().Name, Rule: "requires a value"}
	}
	if invalid, ok := errors.AsType[*pflag.InvalidValueError](err); ok {
		rule, ok := cli.ValueRule(invalid.GetFlag().Value.Type())
		if !ok {
			rule = "use a supported value"
		}
		return &usageError{Option: "--" + invalid.GetFlag().Name, Rule: rule}
	}
	return &usageError{Option: "arguments", Rule: "use only supported options"}
}
