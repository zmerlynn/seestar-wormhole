package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

func newDiscoveryCommand(o *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "discovery",
		Short: "Answer LAN discovery scans with a supplied identity",
		Long: `Answer UDP 4720 scans only; TCP forwarding must be provided separately.
Serial and model are advertised values, not cloud catalog selectors.
No credentials, cloud connection, session or TCP listeners are used.
Stop with Ctrl+C. Logs and errors go to stderr; help and version go to stdout.
Exit 0 on orderly shutdown, 2 on usage/configuration errors, or 1 on runtime errors.`,
		Args: noArguments,
		RunE: func(*cobra.Command, []string) error {
			return validateDiscoveryOptions(o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.bind, "bind", defaultBind, "required local LAN IPv4 address or 0.0.0.0; selects discovery interfaces and reply source")
	f.StringVar(&o.serial, "advertise-serial", "", "required serial to include in discovery replies (no catalog lookup)")
	f.StringVar(&o.model, "advertise-model", "", "required model label to include in discovery replies (no catalog lookup)")
	f.StringVar(&o.name, "advertise-name", "", "display name in discovery replies (empty uses the model label)")
	f.BoolVar(&o.lanDiscoveryAllInterfaces, "lan-discovery-all-interfaces", false, "include VPN and virtual adapters in discovery; all other admission checks still apply")
	registerLoggingOptions(f, o)
	return cmd
}

func validateDiscoveryOptions(o *options) error {
	if err := validateLoggingOptions(o); err != nil {
		return err
	}
	if strings.TrimSpace(o.serial) == "" {
		return &argumentError{Option: "--advertise-serial", Rule: "a nonblank advertised serial is required"}
	}
	if strings.TrimSpace(o.model) == "" {
		return &argumentError{Option: "--advertise-model", Rule: "a nonblank advertised model is required"}
	}
	if !landiscovery.ValidBind(o.bind) {
		return &argumentError{Option: "--bind", Rule: "LAN discovery requires a literal LAN IPv4 address or 0.0.0.0"}
	}
	_, _, err := discoveryMachine(o)
	return err
}

func discoveryMachine(o *options) (*landiscovery.Machine, []string, error) {
	if !utf8.ValidString(o.serial) || !utf8.ValidString(o.model) || !utf8.ValidString(o.name) {
		return nil, nil, &argumentError{Option: "arguments", Rule: "advertised identity must be valid UTF-8 and fit a 4096-byte reply"}
	}
	m, synthesized, err := landiscovery.New(o.serial, o.name, o.model)
	if err != nil {
		return nil, nil, &argumentError{Option: "arguments", Rule: "advertised identity must be valid UTF-8 and fit a 4096-byte reply"}
	}
	return m, synthesized, nil
}

func runDiscovery(ctx context.Context, o options, logs io.Writer, listen func(string, int, landiscovery.Options, *slog.Logger) (commandDiscovery, error)) (result error) {
	defer func() {
		if result == nil {
			return
		}
		var detail *argumentError
		if errors.As(result, &detail) {
			logConfigurationError(logs, "discovery", result)
			return
		}
		diagnostic.LogOutcome(newLogger("json", logs), "discovery failed", "lan_discovery", result, nil)
	}()
	startup := newStartupLogging(o, logs)
	defer func() { result = errors.Join(result, startup.finish(result)) }()
	m, synthesized, err := discoveryMachine(&o)
	if err != nil {
		return err
	}
	s, err := listen(o.bind, landiscovery.Port, landiscovery.Options{AllInterfaces: o.lanDiscoveryAllInterfaces}, startup.logger)
	if err != nil {
		return &argumentError{Option: "--bind", Rule: "use an OS-supported LAN bind address with UDP discovery available"}
	}
	defer s.Close()
	startup.output.resolve(true)
	startup.logger.Warn("lan_discovery_synthesized", "fields", synthesized, "envelope_fields", []string{"Timestamp"})
	startup.logger.Info("lan_discovery_started", "port", landiscovery.Port, "all_interfaces", o.lanDiscoveryAllInterfaces)
	return diagnostic.Wrap("lan_discovery", "", s.Serve(ctx, m))
}
