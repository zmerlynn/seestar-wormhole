// Command seestar-wormhole provides remote access to Seestar devices.
package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/licenses"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

var revision = "development"
var version = "development"

type mappings []proxyrun.Port

func (m *mappings) Type() string { return "local:device" }
func (m *mappings) String() string {
	parts := make([]string, len(*m))
	for i, p := range *m {
		parts[i] = strconv.Itoa(int(p.Local)) + ":" + strconv.Itoa(int(p.Device))
	}
	return strings.Join(parts, ",")
}
func (m *mappings) Set(s string) error {
	ports, err := proxyrun.ParsePort(*m, s)
	if err == nil {
		*m = ports
	}
	return err
}

type options struct {
	command                   commandKind
	outcome                   commandOutcome
	bind, format              string
	level                     string
	ports                     mappings
	idle                      time.Duration
	pauseThreshold            int
	version                   bool
	lanDiscovery              bool
	lanDiscoveryAllInterfaces bool
	serial, model, stateDir   string
	name                      string
}

func validateOptions(o *options) error {
	if o.idle < 0 {
		return &argumentError{Option: "--idle", Rule: "duration must be nonnegative"}
	}
	if o.pauseThreshold < 2 || o.pauseThreshold > proxy.MaxPauseThreshold {
		return &argumentError{Option: "--pause-threshold", Rule: "byte count must be 2..8388608"}
	}
	if err := validateSharedOptions(o); err != nil {
		return err
	}
	if o.lanDiscoveryAllInterfaces && !o.lanDiscovery {
		return &argumentError{Option: "--lan-discovery-all-interfaces", Rule: "requires --lan-discovery"}
	}
	if o.lanDiscovery && !landiscovery.ValidBind(o.bind) {
		return &argumentError{Option: "--bind", Rule: "LAN discovery requires a literal LAN IPv4 address or 0.0.0.0"}
	}
	ports, err := proxyrun.MergePorts(o.ports)
	if err != nil {
		return err
	}
	o.ports = ports
	return nil
}
func run(ctx context.Context, args []string, out io.Writer, logOutput io.Writer) error {
	return runWithSession(ctx, args, out, logOutput, func(ctx context.Context, cfg proxy.Config) (commandSession, error) { return proxy.New(ctx, cfg) })
}

// commandSession is the owned lifetime used by the command. The seam permits
// deterministic construction/cleanup schedules without cloud credentials.
type commandSession interface {
	proxy.Dialer
	Close() error
	Done() <-chan struct{}
	DeviceDetail() cloud.Detail
}

// commandDiscovery permits startup tests without an active LAN or a fixed UDP port.
type commandDiscovery interface {
	Serve(context.Context, *landiscovery.Machine) error
	Close() error
}

func listenCommandDiscovery(bind string, port int, options landiscovery.Options, logger *slog.Logger) (commandDiscovery, error) {
	s, err := landiscovery.Listen(bind, port, options)
	if err != nil {
		return nil, err
	}
	s.Logger = logger
	return s, nil
}

func runWithSession(ctx context.Context, args []string, out io.Writer, logOutput io.Writer, newSession func(context.Context, proxy.Config) (commandSession, error)) error {
	return runWithListener(ctx, args, out, logOutput, newSession, net.Listen)
}

func runWithListener(ctx context.Context, args []string, out io.Writer, logOutput io.Writer, newSession func(context.Context, proxy.Config) (commandSession, error), listen func(string, string) (net.Listener, error)) error {
	o, err := parse(args, out)
	if err != nil {
		if !errors.Is(err, errHelp) {
			logCommandArgumentError(o, logOutput, err)
		}
		return err
	}
	return runParsedWithListener(ctx, o, out, logOutput, newSession, listen)
}

func runParsed(ctx context.Context, o options, out, logs io.Writer) error {
	return runParsedWithListener(ctx, o, out, logs, func(ctx context.Context, cfg proxy.Config) (commandSession, error) { return proxy.New(ctx, cfg) }, net.Listen)
}

func runParsedWithListener(ctx context.Context, o options, out, logs io.Writer, newSession func(context.Context, proxy.Config) (commandSession, error), listen func(string, string) (net.Listener, error)) error {
	return runParsedWithDiscovery(ctx, o, out, logs, newSession, listen, listenCommandDiscovery)
}

func runParsedWithDiscovery(ctx context.Context, o options, out, logs io.Writer, newSession func(context.Context, proxy.Config) (commandSession, error), listen func(string, string) (net.Listener, error), listenDiscovery func(string, int, landiscovery.Options, *slog.Logger) (commandDiscovery, error)) error {
	if o.outcome == commandHelp || o.outcome == commandCompletion {
		return errHelp
	}
	if o.outcome == commandVersion {
		_, err := fmt.Fprintln(out, versionString())
		return err
	}
	if o.command == commandLicenses {
		return licenses.Write(out)
	}
	if o.command == commandUI {
		return runUICommand(ctx, o, out, logs)
	}
	if o.command == commandDiscoveryOnly {
		return runDiscovery(ctx, o, logs, listenDiscovery)
	}
	return runProxy(ctx, o, logs, newSession, listen, listenDiscovery, proxy.CheckTCPStateSupport)
}

func runProxy(ctx context.Context, o options, logOutput io.Writer, newSession func(context.Context, proxy.Config) (commandSession, error), listen func(string, string) (net.Listener, error), listenDiscovery func(string, int, landiscovery.Options, *slog.Logger) (commandDiscovery, error), checkTCPState func(context.Context) error) (result error) {
	phase := "arguments"
	var cleanupErr error
	defer func() {
		if result != nil && !errors.Is(result, errHelp) {
			// The contracted final record is always JSON, independently of the
			// lifecycle format, threshold, startup buffering, and receipt history.
			logger := newLogger("json", logOutput)
			if phase == "tcp_state_query" {
				logger = logger.With("rule", "SIO_TCP_INFO_required", "requirement", "Windows 10 version 1703 or later, or Windows Server 2016 or later")
			}
			var detail *argumentError
			if errors.As(result, &detail) {
				// Startup resolution follows the same JSON-only configuration contract as
				// parsing, irrespective of requested log format, level or mappings.
				logger = configurationLogger(logOutput, "proxy", detail)
				logger.Error("proxy failed", "phase", "arguments", "reason", "invalid_option", "cleanup_reason", diagnostic.CleanupClass(cleanupErr))
				return
			}
			diagnostic.LogOutcome(logger, "proxy failed", phase, result, cleanupErr)
		}
	}()
	startup := newStartupLogging(o, logOutput)
	logger := startup.logger
	defer func() { result = errors.Join(result, startup.finish(result)) }()
	var err error
	phase = "tcp_state_query"
	if err := checkTCPState(ctx); err != nil {
		return err
	}
	phase = "listener"
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var forwards []proxy.Forward
	defer func() {
		for _, f := range forwards {
			f.Listener.Close()
		}
	}()
	for _, p := range o.ports {
		l, err := listen("tcp", net.JoinHostPort(o.bind, strconv.Itoa(int(p.Local))))
		if err != nil {
			return &diagnostic.OptionError{Option: "--bind", Rule: "use an OS-supported bind address with available local ports"}
		}
		forwards = append(forwards, proxy.Forward{Listener: l, Port: p.Device})
	}
	var discovery commandDiscovery
	if o.lanDiscovery {
		phase = "lan_discovery"
		discovery, err = listenDiscovery(o.bind, landiscovery.Port, landiscovery.Options{AllInterfaces: o.lanDiscoveryAllInterfaces}, logger)
		if err != nil {
			return &diagnostic.OptionError{Option: "--bind", Rule: "use an OS-supported LAN bind address with UDP discovery available"}
		}
		defer discovery.Close()
	}
	phase = "session"
	s, err := newSession(ctx, proxy.Config{LANDiscovery: o.lanDiscovery, Serial: o.serial, Model: o.model, StateDir: o.stateDir, PauseThreshold: o.pauseThreshold, Idle: o.idle, Logger: logger})
	if err != nil {
		return err
	}
	// Always attempt cloud stop even when listener setup or a worker fails.
	defer func() {
		outcome := s.Close()
		result = errors.Join(result, outcome)
		cleanupErr = diagnostic.CleanupError(outcome)
		logger.Info("proxy_cleanup_complete", "cleanup_reason", diagnostic.CleanupClass(cleanupErr))
	}()
	var discoveryDone chan struct{}
	var discoveryErr error
	if discovery != nil {
		phase = "lan_discovery"
		d := s.DeviceDetail()
		machine, synthesized, err := landiscovery.New(d.Serial(), d.Name(), d.Model())
		if err != nil {
			return diagnostic.Wrap("lan_discovery", "invalid_device_detail", err)
		}
		startup.output.resolve(true)
		logger.Warn("lan_discovery_synthesized", "fields", synthesized, "envelope_fields", []string{"Timestamp"})
		discoveryDone = make(chan struct{})
		go func() { discoveryErr = discovery.Serve(ctx, machine); close(discoveryDone) }()
		// Join discovery before closing the session or returning from any branch.
		defer func() { cancel(); discovery.Close(); <-discoveryDone }()
		logger.Info("lan_discovery_started", "port", landiscovery.Port, "all_interfaces", o.lanDiscoveryAllInterfaces)
		warnLANHTTP(logger, o.ports)
	}
	// Configuration is accepted before waiting for readiness, which may spend
	// a long time retrying busy. Keep those lifecycle diagnostics visible.
	startup.output.resolve(true)
	warnWildcardBind(logger, forwards, systemInterfaces())
	done := make(chan error, 1)
	go func() { done <- proxy.Serve(ctx, s, forwards, 128, logger) }()
	logger.Info("proxy_started", "revision", revision, "listeners", len(forwards), "lazy", o.idle > 0, "pause_threshold", o.pauseThreshold)
	for {
		select {
		case <-discoveryDone:
			cancel()
			err = <-done
			return errors.Join(diagnostic.Wrap("lan_discovery", "", discoveryErr), err)
		case err = <-done:
			cancel()
			return err
		case <-s.Done():
			cancel()
			return <-done
		case <-ctx.Done():
			cancel()
			return <-done
		}
	}
}

// The phone builds album/FITS URLs without a port.
func warnLANHTTP(logger *slog.Logger, ports mappings) {
	for _, p := range ports {
		if p.Local == 80 && p.Device == 80 {
			return
		}
	}
	logger.Warn("lan_http_port", "option", "-p 80:80", "warning", "album/image views need -p 80:80")
}

func main() {
	// Cobra's Explorer hook exits before parsing; this executable owns its help
	// and refusal policy on every launch path.
	cobra.MousetrapHelpText = ""
	os.Exit(mainExitCode())
}

// Keep descriptor and signal cleanup inside a returning function: os.Exit does
// not run defers, including restoration of Darwin inherited stderr flags.
func mainExitCode() int {
	return mainExitCodeWithBootstrap(os.Args[1:], os.Stdout, os.Stderr, signal.NotifyContext, logWriter)
}

func mainExitCodeWithBootstrap(args []string, out, logOutput io.Writer, notify func(context.Context, ...os.Signal) (context.Context, context.CancelFunc), newLog func() (io.Writer, func())) int {
	return mainExitCodeWithPlatform(args, out, logOutput, notify, newLog, runtime.GOOS == "windows")
}

func mainExitCodeWithPlatform(args []string, out, logOutput io.Writer, notify func(context.Context, ...os.Signal) (context.Context, context.CancelFunc), newLog func() (io.Writer, func()), windows bool) int {
	return mainExitCodeWithRunner(args, out, logOutput, notify, newLog, windows, runParsed)
}

// The parsed options cross this boundary exactly once; runtime and exit policy
// must never independently reinterpret the original argument positions.
func mainExitCodeWithRunner(args []string, out, logOutput io.Writer, notify func(context.Context, ...os.Signal) (context.Context, context.CancelFunc), newLog func() (io.Writer, func()), windows bool, execute func(context.Context, options, io.Writer, io.Writer) error) int {
	o, err := parse(args, out)
	if errors.Is(err, errHelp) {
		return 0
	}
	if err == nil && o.outcome == commandVersion {
		_, err = fmt.Fprintln(out, versionString())
		return commandExitCode(o, err)
	}
	if err == nil && o.command == commandLicenses {
		return commandExitCode(o, licenses.Write(out))
	}
	// Windows refuses invalid options before acquiring signal or log handles.
	// Unix keeps nonblocking log setup for argument errors on stalled stderr.
	if err != nil && windows {
		logCommandArgumentError(o, logOutput, err)
		return commandExitCode(o, err)
	}

	// Go maps Windows Ctrl+C/Ctrl+Break to Interrupt and console close/logoff
	// to SIGTERM. Console close still has an OS termination deadline: cleanup
	// is best effort there; Ctrl+C lets the normal bounded release finish.
	signals := []os.Signal{os.Interrupt, syscall.SIGTERM}
	if o.command == commandUI && !windows {
		// Closing a Unix terminal must release the UI's session before exit.
		signals = append(signals, syscall.SIGHUP)
	}
	ctx, stop := notify(context.Background(), signals...)
	defer stop()
	// Acquire stdout first so Darwin restores shared stdout/stderr flags in
	// reverse acquisition order, including launches using 2>&1.
	var fallback io.Writer = out
	if out == os.Stdout {
		var closeFallback func()
		fallback, closeFallback = logWriterFD(1)
		defer closeFallback()
	}
	sink, closeLog := newLog()
	closeLog = sync.OnceFunc(closeLog)
	defer closeLog()
	w, ok := sink.(*logLossWriter)
	if !ok {
		w = &logLossWriter{output: sink}
	}
	w.fallback = fallback
	if q, ok := w.output.(interface{ setLogReporter(*logLossWriter) }); ok {
		q.setLogReporter(w)
	}
	if err != nil {
		logCommandArgumentError(o, w, err)
	} else {
		err = execute(ctx, o, out, w)
	}
	closeLog()
	if loss := w.Err(); loss != nil {
		newLogger("json", w.fallback).Warn("diagnostics_lost", "rule", "log_sink_loss", "count", w.Dropped(), "bytes", w.bytes.Load())
		if err == nil {
			return 1
		}
	}
	return commandExitCode(o, err)
}

func logCommandArgumentError(o options, out io.Writer, err error) {
	switch o.command {
	case commandUI:
		logConfigurationError(out, "ui", err)
	case commandLicenses:
		logConfigurationError(out, "licenses", err)
	case commandDiscoveryOnly:
		logConfigurationError(out, "discovery", err)
	default:
		logArgumentError(out, err)
	}
}

func commandExitCode(o options, err error) int {
	if err == nil || errors.Is(err, errHelp) {
		return 0
	}
	var detail *argumentError
	if errors.As(err, &detail) || errors.Is(err, cloud.ErrConfig) {
		return 2
	}
	phase, reason := diagnostic.Fields(err, "")
	if phase == "cloud" && (reason == "unauthorized" || reason == "reauthentication_required") {
		return 3
	}
	return 1
}
