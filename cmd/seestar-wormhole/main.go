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

type mapping struct{ local, device uint16 }
type mappings []mapping

func (m *mappings) Type() string { return "local:device" }
func (m *mappings) String() string {
	parts := make([]string, len(*m))
	for i, p := range *m {
		parts[i] = strconv.Itoa(int(p.local)) + ":" + strconv.Itoa(int(p.device))
	}
	return strings.Join(parts, ",")
}
func runnerPorts(m mappings) []proxyrun.Port {
	ports := make([]proxyrun.Port, len(m))
	for i, p := range m {
		ports[i] = proxyrun.Port{Local: p.local, Device: p.device}
	}
	return ports
}
func (m *mappings) Set(s string) error {
	ports, err := proxyrun.ParsePort(runnerPorts(*m), s)
	if err != nil {
		return err
	}
	*m = nil
	for _, p := range ports {
		*m = append(*m, mapping{p.Local, p.Device})
	}
	return nil
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
	ports, err := proxyrun.MergePorts(runnerPorts(o.ports))
	if err != nil {
		return err
	}
	o.ports = nil
	for _, p := range ports {
		o.ports = append(o.ports, mapping{p.Local, p.Device})
	}
	return nil
}
func run(ctx context.Context, args []string, out io.Writer, logOutput io.Writer) error {
	return runWithSession(ctx, args, out, logOutput, func(ctx context.Context, cfg proxy.Config) (commandSession, error) { return proxy.New(ctx, cfg) })
}

// commandSession is the owned lifetime used by the command. The seam permits
// deterministic construction/cleanup schedules without cloud credentials.
type commandSession = proxyrun.Session

// commandDiscovery permits startup tests without an active LAN or a fixed UDP port.
type commandDiscovery = proxyrun.Discovery

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
	ports := make([]proxyrun.Port, len(o.ports))
	for i, p := range o.ports {
		ports[i] = proxyrun.Port{Local: p.local, Device: p.device}
	}
	return proxyrun.Run(ctx, proxyrun.Config{Bind: o.bind, Revision: revision, Ports: ports, AllInterfaces: o.lanDiscoveryAllInterfaces, Session: proxy.Config{LANDiscovery: o.lanDiscovery, Serial: o.serial, Model: o.model, StateDir: o.stateDir, PauseThreshold: o.pauseThreshold, Idle: o.idle, Logger: logger}}, proxyrun.Hooks{
		Phase: &phase, Cleanup: &cleanupErr, NewSession: newSession, Listen: listen, ListenDiscovery: listenDiscovery, CheckTCPState: checkTCPState,
		Accepted: func() { startup.output.resolve(true) }, WarnLANHTTP: func() { warnLANHTTP(logger, o.ports) }, WarnBind: func(f []proxy.Forward) { warnWildcardBind(logger, f, systemInterfaces()) },
	})
}

// The phone builds album/FITS URLs without a port.
func warnLANHTTP(logger *slog.Logger, ports mappings) {
	for _, p := range ports {
		if p.local == 80 && p.device == 80 {
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
	ctx, stop := notify(context.Background(), os.Interrupt, syscall.SIGTERM)
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
