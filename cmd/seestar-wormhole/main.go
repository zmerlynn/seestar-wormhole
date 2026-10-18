// Command seestar-wormhole provides remote access to Seestar devices.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/licenses"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
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
func (m *mappings) Set(s string) error {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return &argumentError{Option: "--port", Rule: "use local:device ports"}
	}
	if len(*m) >= 128 {
		return &argumentError{Option: "--port", Rule: "at most 128 mappings are allowed"}
	}
	a, e := strconv.ParseUint(parts[0], 10, 16)
	if e != nil {
		return &argumentError{Option: "--port", Rule: "local port must be 0..65535"}
	}
	b, e := strconv.ParseUint(parts[1], 10, 16)
	if e != nil || b == 0 {
		return &argumentError{Option: "--port", Rule: "device port must be 1..65535"}
	}
	for _, p := range *m {
		if p.device == uint16(b) {
			return &argumentError{Option: "--port", Rule: "device ports must be unique"}
		}
		if a != 0 && p.local == uint16(a) {
			return &argumentError{Option: "--port", Rule: "local ports must be unique"}
		}
	}
	*m = append(*m, mapping{uint16(a), uint16(b)})
	return nil
}

type options struct {
	command                 commandKind
	outcome                 commandOutcome
	pull                    *pullOptions
	bind, format            string
	keylog                  string
	trace                   string
	level                   string
	ports                   mappings
	idle                    time.Duration
	pauseThreshold          int
	statsInterval           time.Duration
	version                 bool
	lanDiscovery            bool
	serial, model, stateDir string
}

func validateOptions(o *options, supported func() error) error {
	if o.idle < 0 {
		return &argumentError{Option: "--idle", Rule: "duration must be nonnegative"}
	}
	if o.statsInterval < 0 {
		return &argumentError{Option: "--stats-interval", Rule: "duration must be nonnegative"}
	}
	if o.pauseThreshold < 2 || o.pauseThreshold > proxy.MaxPauseThreshold {
		return &argumentError{Option: "--pause-threshold", Rule: "byte count must be 2..8388608"}
	}
	if err := validateSharedOptions(o); err != nil {
		return err
	}
	if o.lanDiscovery && supported() != nil {
		return &argumentError{Option: "--lan-discovery", Rule: "LAN discovery is unsupported on Windows"}
	}
	if o.lanDiscovery && !landiscovery.ValidBind(o.bind) {
		return &argumentError{Option: "--bind", Rule: "LAN discovery requires a literal LAN IPv4 address or 0.0.0.0"}
	}
	defaults := mappings{{4700, 4700}, {4445, 445}, {8080, 80}, {4350, 4350}, {4554, 4554}, {4555, 4555}, {4801, 4801}, {4800, 4800}, {4804, 4804}, {4361, 4361}}
	overrides := o.ports
	o.ports = nil
	for _, p := range defaults {
		for _, v := range overrides {
			if p.device == v.device {
				p = v
				break
			}
		}
		if p.local != 0 {
			o.ports = append(o.ports, p)
		}
	}
	for _, v := range overrides {
		known := false
		for _, p := range defaults {
			if p.device == v.device {
				known = true
				break
			}
		}
		if !known && v.local != 0 {
			o.ports = append(o.ports, v)
		}
	}
	if len(o.ports) > 128 {
		return &argumentError{Option: "--port", Rule: "at most 128 mappings are allowed"}
	}
	locals := make(map[uint16]bool)
	for _, p := range o.ports {
		if locals[p.local] {
			return &argumentError{Option: "--port", Rule: "local ports must be unique"}
		}
		locals[p.local] = true
	}
	return nil
}
func run(ctx context.Context, args []string, out io.Writer, logOutput io.Writer) error {
	return runWithKeyLog(ctx, args, out, logOutput, keylog.Open)
}

// commandSession is the owned lifetime used by the command. The seam permits
// deterministic construction/cleanup schedules without cloud credentials.
type commandSession interface {
	proxy.Dialer
	Close() error
	Done() <-chan struct{}
	DeviceDetail() cloud.Detail
}

func runWithKeyLog(ctx context.Context, args []string, out io.Writer, logOutput io.Writer, openKeys func(string, *slog.Logger) (*keylog.Writer, error)) error {
	return runWithSession(ctx, args, out, logOutput, openKeys, func(ctx context.Context, cfg proxy.Config) (commandSession, error) { return proxy.New(ctx, cfg) })
}

func runWithSession(ctx context.Context, args []string, out io.Writer, logOutput io.Writer, openKeys func(string, *slog.Logger) (*keylog.Writer, error), newSession func(context.Context, proxy.Config) (commandSession, error)) error {
	return runWithListener(ctx, args, out, logOutput, openKeys, newSession, net.Listen)
}

func runWithListener(ctx context.Context, args []string, out io.Writer, logOutput io.Writer, openKeys func(string, *slog.Logger) (*keylog.Writer, error), newSession func(context.Context, proxy.Config) (commandSession, error), listen func(string, string) (net.Listener, error)) error {
	o, err := parse(args, out)
	if err != nil {
		if !errors.Is(err, errHelp) {
			logCommandArgumentError(o, logOutput, err)
		}
		return err
	}
	return runParsedWithListener(ctx, o, out, logOutput, openKeys, newSession, listen)
}

func runParsed(ctx context.Context, o options, out, logs io.Writer) error {
	return runParsedWithListener(ctx, o, out, logs, keylog.Open, func(ctx context.Context, cfg proxy.Config) (commandSession, error) { return proxy.New(ctx, cfg) }, net.Listen)
}

func runParsedWithListener(ctx context.Context, o options, out, logs io.Writer, openKeys func(string, *slog.Logger) (*keylog.Writer, error), newSession func(context.Context, proxy.Config) (commandSession, error), listen func(string, string) (net.Listener, error)) error {
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
	if o.command == commandPull {
		return runParsedPullWithSource(ctx, o, out, logs, openKeys, newSession, pull.OpenSMB)
	}
	return runProxy(ctx, o, logs, openKeys, newSession, listen)
}

func runProxy(ctx context.Context, o options, logOutput io.Writer, openKeys func(string, *slog.Logger) (*keylog.Writer, error), newSession func(context.Context, proxy.Config) (commandSession, error), listen func(string, string) (net.Listener, error)) (result error) {
	var err error
	phase := "arguments"
	var cleanupErr error
	defer func() {
		if result != nil && !errors.Is(result, errHelp) {
			// The contracted final record is always JSON, independently of the
			// lifecycle format, threshold, startup buffering, and receipt history.
			logger := newLogger("json", logOutput)
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
	defer func() { startup.finish(result) }()
	var trace *tracerecord.Writer
	if o.trace != "" {
		phase = "trace"
		trace, err = openTrace(o.trace, o.ports)
		if err != nil {
			return &argumentError{Option: "--trace", Rule: "use a new writable trace file"}
		}
		defer trace.Close()
	}
	keys := startup.openKeys(o.keylog, openKeys)
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
		l, err := listen("tcp", net.JoinHostPort(o.bind, strconv.Itoa(int(p.local))))
		if err != nil {
			return &argumentError{Option: "--bind", Rule: "use an OS-supported bind address with available local ports"}
		}
		forwards = append(forwards, proxy.Forward{Listener: l, Port: p.device})
	}
	var discovery *landiscovery.Socket
	if o.lanDiscovery {
		phase = "lan_discovery"
		discovery, err = landiscovery.Listen(o.bind, landiscovery.Port)
		if err != nil {
			return &argumentError{Option: "--bind", Rule: "use an OS-supported LAN bind address with UDP discovery available"}
		}
		discovery.Trace = trace
		defer discovery.Close()
	}
	phase = "session"
	s, err := newSession(ctx, proxy.Config{Trace: trace, LANDiscovery: o.lanDiscovery, Serial: o.serial, Model: o.model, StateDir: o.stateDir, StatsInterval: o.statsInterval, PauseThreshold: o.pauseThreshold, Idle: o.idle, Logger: logger, KeyLog: keys})
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
		logger.Info("lan_discovery_started", "port", landiscovery.Port)
		warnLANHTTP(logger, o.ports)
	}
	// Configuration is accepted before waiting for readiness, which may spend
	// a long time retrying busy. Keep those lifecycle diagnostics visible.
	startup.output.resolve(true)
	warnWildcardBind(logger, forwards, systemInterfaces())
	startup.acceptKeys()
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
	return mainExitCodeWithPlatform(args, out, logOutput, notify, newLog, landiscovery.Supported)
}

func mainExitCodeWithPlatform(args []string, out, logOutput io.Writer, notify func(context.Context, ...os.Signal) (context.Context, context.CancelFunc), newLog func() (io.Writer, func()), supported func() error) int {
	return mainExitCodeWithRunner(args, out, logOutput, notify, newLog, supported, runParsed)
}

// The parsed options cross this boundary exactly once; runtime and exit policy
// must never independently reinterpret the original argument positions.
func mainExitCodeWithRunner(args []string, out, logOutput io.Writer, notify func(context.Context, ...os.Signal) (context.Context, context.CancelFunc), newLog func() (io.Writer, func()), supported func() error, execute func(context.Context, options, io.Writer, io.Writer) error) int {
	o, err := parseWithSupport(args, out, supported)
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
	if err != nil && supported() != nil {
		logCommandArgumentError(o, logOutput, err)
		return commandExitCode(o, err)
	}

	// Go maps Windows Ctrl+C/Ctrl+Break to Interrupt and console close/logoff
	// to SIGTERM. Console close still has an OS termination deadline: cleanup
	// is best effort there; Ctrl+C lets the normal bounded release finish.
	ctx, stop := notify(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	w, closeLog := newLog()
	defer closeLog()
	if err != nil {
		logCommandArgumentError(o, w, err)
	} else {
		err = execute(ctx, o, out, w)
	}
	return commandExitCode(o, err)
}

func logCommandArgumentError(o options, out io.Writer, err error) {
	if o.command == commandPull {
		logPullArgumentError(out, err)
	} else if o.command == commandLicenses {
		logConfigurationError(out, "licenses", err)
	} else {
		logArgumentError(out, err)
	}
}

func commandExitCode(o options, err error) int {
	if err == nil || errors.Is(err, errHelp) {
		return 0
	}
	if o.command == commandPull {
		return pullCode(err)
	}
	var detail *argumentError
	if errors.As(err, &detail) || errors.Is(err, cloud.ErrConfig) {
		return 2
	}
	return 1
}

// openTrace records mapped device destinations as protocol facts. Only the
// destination side is configured; a remapped local listener stays private.
func openTrace(path string, ports mappings) (*tracerecord.Writer, error) {
	w, err := tracerecord.Open(path)
	if err != nil {
		return nil, err
	}
	servicePorts := make([]uint16, 0, len(ports))
	for _, p := range ports {
		servicePorts = append(servicePorts, p.device)
	}
	w.SetServicePorts(servicePorts)
	return w, nil
}
