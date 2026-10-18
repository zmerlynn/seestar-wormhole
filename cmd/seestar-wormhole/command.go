package main

import (
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
)

var errHelp = errors.New("help requested")

type commandKind uint8

const (
	commandRoot commandKind = iota
	commandProxy
	commandLicenses
)

type commandOutcome uint8

const (
	commandRun commandOutcome = iota
	commandHelp
	commandVersion
	commandCompletion
)

func versionString() string {
	return "seestar-wormhole " + version + " (" + revision + ", " + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

// newCommand keeps parsing and pure validation ahead of every runtime resource.
// Flags are local to each command, including logging and connection options.
func newCommand(o *options, out io.Writer, supported func() error) *cobra.Command {
	root := &cobra.Command{
		Use:                "seestar-wormhole",
		Short:              "Remote access to Seestar devices",
		SilenceErrors:      true,
		SilenceUsage:       true,
		DisableSuggestions: true,
		CompletionOptions:  cobra.CompletionOptions{DisableDefaultCmd: true},
		Args:               noArguments,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.version {
				return nil
			}
			return cmd.Help()
		},
	}
	root.SetOut(out)
	root.SetErr(io.Discard)
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		defaultHelp(cmd, args)
		if cmd == root {
			fmt.Fprintln(cmd.OutOrStdout(), "Third-party license information: seestar-wormhole licenses")
		}
	})
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return flagParseError(err) })
	root.Flags().BoolVar(&o.version, "version", false, "print version, revision and platform")
	proxyCommand := &cobra.Command{
		Use:   "proxy",
		Short: "Forward local TCP ports through one shared Seestar session",
		Long: `Forward local TCP ports through one shared Seestar session.
Zero idle duration eagerly acquires and holds the session; a positive duration
opens on client demand and releases when idle. Stop with Ctrl+C.
Credentials use SEESTAR_EMAIL and SEESTAR_PASSWORD from the environment only.
The app cannot change the device's site, clock or time zone through the proxy.
Durations use Go units, e.g. 2m or 1h30m, and must be nonnegative (no d unit).
Logs and errors go to stderr; help and version go to stdout.
Exit 0 on orderly release, 2 on usage/configuration errors,
or 1 on runtime errors including failed release.`,
		Example: "  seestar-wormhole proxy --idle 2m --state-dir /private/seestar",
		Args:    noArguments,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return validateOptions(o, supported)
		},
	}
	f := proxyCommand.Flags()
	registerSharedOptions(f, o)
	f.StringVar(&o.bind, "bind", "127.0.0.2", "local bind address (use 0.0.0.0 for a container)")
	f.VarP(&o.ports, "port", "p", "override a device listener with local:device; local 0 disables; repeatable (defaults 4700:4700, SMB 4445:445, HTTP 8080:80; 4350:4350,4554:4554,4555:4555,4801:4801,4800:4800,4804:4804,4361:4361)")
	f.IntVar(&o.pauseThreshold, "pause-threshold", p2ptunnel.DefaultConfig().HighWater, "per-channel PAUSE pressure in bytes (2..8388608; RESUME below 80%)")
	f.DurationVar(&o.statsInterval, "stats-interval", time.Second, "transport stats interval; zero disables counters and debug telemetry")
	f.DurationVar(&o.idle, "idle", 0, "release after this idle duration; zero holds eagerly")
	f.BoolVar(&o.lanDiscovery, "lan-discovery", false, "answer UDP 4720 discovery on the bind address LAN (off by default)")
	root.AddCommand(proxyCommand, &cobra.Command{
		Use:   "licenses",
		Short: "View license information for third-party libraries used in this build",
		Args:  noArguments,
		RunE:  func(*cobra.Command, []string) error { return nil },
	})
	return root
}

func noArguments(_ *cobra.Command, args []string) error {
	if len(args) != 0 {
		return &argumentError{Option: "arguments", Rule: "positional arguments are not allowed"}
	}
	return nil
}

func parse(args []string, out io.Writer) (options, error) {
	return parseWithSupport(args, out, landiscovery.Supported)
}

func parseWithSupport(args []string, out io.Writer, supported func() error) (options, error) {
	var o options
	command := newCommand(&o, out, supported)
	// An explicit empty invocation must not fall back to the process arguments.
	command.SetArgs(append([]string{}, args...))
	executed, err := command.ExecuteC()
	// Preserve Cobra's resolution even when parsing or validation fails, so
	// diagnostics and exit policy belong to the same command as execution.
	switch executed.Name() {
	case "proxy":
		o.command = commandProxy
	case "licenses":
		o.command = commandLicenses
	}
	if err != nil {
		return o, flagParseError(err)
	}
	help, _ := executed.Flags().GetBool("help")
	switch {
	case help || executed.Name() == "help":
		o.outcome = commandHelp
	case executed.Name() == cobra.ShellCompRequestCmd:
		o.outcome = commandCompletion
	case o.version:
		o.outcome = commandVersion
	case executed == command:
		o.outcome = commandHelp
	}
	if o.outcome == commandHelp || o.outcome == commandCompletion {
		return o, errHelp
	}
	return o, nil
}
