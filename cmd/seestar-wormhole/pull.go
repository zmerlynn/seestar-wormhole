package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"io"
	"log/slog"
	"math"
	"net"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
)

type pullOptions struct {
	config            pull.Config
	busyWait, timeout time.Duration
	bwlimit           string
	via               string
	output            string
	rebind            bool
	progress          bool
}

func newPullCommand(o *options) *cobra.Command {
	p := &pullOptions{config: pull.DefaultConfig()}
	o.pull = p
	cmd := &cobra.Command{
		Use:   "pull [flags] [--] <dest>",
		Short: "Copy image directories with length checks through a direct session or ambient proxy",
		Long: `Copy EMMC Images/MyWorks into <dest>/<folder>/<relative-file>, preserving _sub.
Direct mode owns one device session in-process. Credentials use SEESTAR_EMAIL and
SEESTAR_PASSWORD from the environment only. Busy fails immediately by default.
--busy-wait bounds initial acquisition and occupancy waits across SMB retries
with one deadline that never renews. After acquisition, transport recovery may
proceed past it; a recovery busy answer then fails fast (exit 3).
--via host:port instead uses an ambient proxy SMB endpoint, with no credentials,
device session ownership or fallback. IPv6 endpoints use [::1]:port. Session
flags cannot be used with --via. Switching sources requires --rebind, which
keeps verified receipts and resets all selection checkpoints.

See docs/reference.md for selection, receipts, layouts, state, output and progress details.`,
		Example: `  seestar-wormhole pull --since 24h --only "Dark.*" /data/last-night
  seestar-wormhole pull --dir MyWorks --dir Autorun/Dark --dir darklibrary ./archive
  seestar-wormhole pull --busy-wait 90s --timeout 30m "D:\Astro Data"`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 && !(o.version && len(args) == 0) {
				return &argumentError{Option: "destination", Rule: "supply exactly one local destination"}
			}
			if len(args) == 1 {
				p.config.Destination = args[0]
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if p.config.DryRun {
				for _, name := range []string{"state-dir", "trace", "keylog", "rebind"} {
					if cmd.Flags().Changed(name) {
						return &argumentError{Option: "--" + name, Rule: "cannot be used with --dry-run"}
					}
				}
			}
			if p.output != "text" && p.output != "json" {
				return &argumentError{Option: "--output", Rule: "use text or json"}
			}
			if cmd.Flags().Changed("via") {
				endpoint, err := normalizeVia(p.via)
				if err != nil {
					return err
				}
				p.via = endpoint
				for _, name := range []string{"serial", "model", "state-dir", "trace", "keylog", "busy-wait"} {
					if cmd.Flags().Changed(name) {
						return &argumentError{Option: "--" + name, Rule: "cannot be used with --via"}
					}
				}
			}
			if cmd.Flags().Changed("until") && p.config.Until == "" {
				return &argumentError{Option: "--until", Rule: "use zoned RFC3339 or a positive Go duration"}
			}
			if o.version && p.config.Destination == "" {
				p.config.Destination = "."
			}
			if err := p.config.Validate(runtime.GOOS); err != nil {
				return err
			}
			if p.busyWait < 0 {
				return &argumentError{Option: "--busy-wait", Rule: "use a nonnegative Go duration"}
			}
			if p.timeout < 0 {
				return &argumentError{Option: "--timeout", Rule: "use a nonnegative Go duration"}
			}
			rate, err := parseBandwidth(p.bwlimit)
			if err != nil {
				return err
			}
			p.config.BytesPerSecond = rate
			return validateSharedOptions(o)
		},
	}
	f := cmd.Flags()
	registerSharedOptions(f, o)
	f.BoolVar(&p.config.DryRun, "dry-run", p.config.DryRun, "plan copy/check/skip/defer with no local writes; per-file stderr at debug")
	f.StringVar(&p.output, "output", "text", "stdout summary format: text or one JSON object")
	f.BoolVar(&p.progress, "progress", false, "show bounded file/byte/rate progress on stderr")
	f.StringVar(&p.via, "via", "", "ambient proxy SMB host:port (including [::1]:4445); no owned session")
	f.BoolVar(&p.rebind, "rebind", false, "take over destination source binding; keep receipts and reset checkpoints")
	f.StringVar(&p.config.Since, "since", p.config.Since, "auto, all, zoned RFC3339 or positive Go age (24h)")
	f.StringVar(&p.config.Until, "until", "", "inclusive upper mtime: zoned RFC3339 or positive Go age; capped by settle")
	f.DurationVar(&p.config.Settle, "settle", p.config.Settle, "defer mtimes newer than run start minus this duration")
	f.DurationVar(&p.config.Overlap, "overlap", p.config.Overlap, "auto checkpoint lookback; zero allowed")
	f.StringArrayVar(&p.config.Dirs, "dir", nil, "share-relative directory under EMMC Images; repeatable; copies into DEST/PATH (default: MyWorks contents directly into DEST)")
	f.StringArrayVar(&p.config.Only, "only", p.config.Only, "quoted unanchored Go regexp (RE2) folder union; repeatable")
	f.BoolVar(&p.config.Recheck, "recheck", p.config.Recheck, "force download verification of eligible files, including matching receipts")
	f.BoolVar(&p.config.Verify, "verify", p.config.Verify, "reread source to compare bytes; upgrade size receipts without recopy if equal")
	f.BoolVar(&p.config.Sums, "sums", p.config.Sums, "write SHA256SUMS of local bytes; false keeps length/opted-in reread checks")
	f.BoolVar(&p.config.ErrorOnEmpty, "error-on-empty", p.config.ErrorOnEmpty, "exit 4 for genuine nothing-new (not deferred)")
	f.IntVar(&p.config.Transfers, "transfers", p.config.Transfers, "concurrent copy/verification workers, 1..8")
	f.StringVar(&p.bwlimit, "bwlimit", "0", "0 unlimited, or explicit bytes/s, KiB/s, MiB/s rate")
	f.DurationVar(&p.busyWait, "busy-wait", 0, "one initial acquisition and occupancy budget across retries (e.g. 90s; zero fails fast on busy)")
	f.DurationVar(&p.timeout, "timeout", 0, "overall work timeout; zero unlimited; release separately bounded")
	return cmd
}

func parseBandwidth(s string) (int64, error) {
	if s == "0" {
		return 0, nil
	}
	var amount, suffix string
	multiplier := float64(1)
	switch {
	case strings.HasSuffix(s, "bytes/s"):
		amount = strings.TrimSuffix(s, "bytes/s")
		suffix = "B"
	case strings.HasSuffix(s, "KiB/s"):
		amount = strings.TrimSuffix(s, "KiB/s")
		suffix = "Ki"
		multiplier = 1024
	case strings.HasSuffix(s, "MiB/s"):
		amount = strings.TrimSuffix(s, "MiB/s")
		suffix = "Mi"
		multiplier = 1048576
	default:
		return 0, &argumentError{Option: "--bwlimit", Rule: "use 0 or positive bytes/s, KiB/s or MiB/s"}
	}
	number, err := strconv.ParseFloat(amount, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number <= 0 || number*multiplier >= float64(math.MaxInt64) {
		return 0, &argumentError{Option: "--bwlimit", Rule: "use a finite positive rate within int64 bytes/s"}
	}
	var rate fs.SizeSuffix
	if err = rate.Set(amount + suffix); err != nil || rate <= 0 {
		return 0, &argumentError{Option: "--bwlimit", Rule: "use a positive bytes/s, KiB/s or MiB/s rate"}
	}
	return int64(rate), nil
}

type pullOutcome struct {
	code  int
	cause error
}

func (e *pullOutcome) Error() string { return "pull failed" }
func (e *pullOutcome) Unwrap() error { return e.cause }

func pullCode(err error) int {
	if err == nil || errors.Is(err, errHelp) {
		return 0
	}
	if errors.Is(err, diagnostic.ErrPullPanic) || errors.Is(err, session.ErrStop) || errors.Is(err, session.ErrUncertain) {
		return 1
	}
	var outcome *pullOutcome
	if errors.As(err, &outcome) {
		return outcome.code
	}
	var detail *diagnostic.PullError
	if errors.As(err, &detail) && (detail.Reason == diagnostic.SourceMismatch || detail.Reason == diagnostic.LayoutMismatch) {
		return 2
	}
	var option *argumentError
	if errors.As(err, &option) || errors.Is(err, cloud.ErrConfig) {
		return 2
	}
	if errors.Is(err, context.Canceled) {
		_, reason := diagnostic.Fields(err, "pull")
		if reason == "canceled" {
			return 130
		}
	}
	if errors.Is(err, session.ErrBusy) || errors.Is(err, pull.ErrLocked) {
		return 3
	}
	return 1
}

func runPull(ctx context.Context, args []string, out, logs io.Writer, openKeys func(string, *slog.Logger) (*keylog.Writer, error), newSession func(context.Context, proxy.Config) (commandSession, error)) (result error) {
	return runPullWithSource(ctx, args, out, logs, openKeys, newSession, pull.OpenSMB)
}

// Source establishment is injectable so phase tests use the production SMB
// backend with deterministic operation barriers instead of timing network traffic.
func runPullWithSource(ctx context.Context, args []string, out, logs io.Writer, openKeys func(string, *slog.Logger) (*keylog.Writer, error), newSession func(context.Context, proxy.Config) (commandSession, error), openSource func(context.Context, func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error)) (result error) {
	o, err := parse(args, out)
	if err != nil {
		if !errors.Is(err, errHelp) {
			logCommandArgumentError(o, logs, err)
		}
		return err
	}
	if o.outcome == commandVersion {
		_, err = fmt.Fprintln(out, versionString())
		return err
	}
	return runParsedPullWithSource(ctx, o, out, logs, openKeys, newSession, openSource)
}

func runParsedPullWithSource(ctx context.Context, o options, out, logs io.Writer, openKeys func(string, *slog.Logger) (*keylog.Writer, error), newSession func(context.Context, proxy.Config) (commandSession, error), openSource func(context.Context, func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error)) (result error) {
	started := time.Now().UTC()
	p := o.pull
	var progressOutput *pullProgressOutput
	stopProgress := func() {}
	if p.progress {
		progressOutput = newPullProgressOutput(logs)
		logs = progressOutput
	}
	startup := newStartupLogging(o, logs)
	logger := startup.logger
	// rclone logs contain unsanitized backend paths. The command reports handled
	// errors through its existing diagnostic boundary and keeps stdout for results.
	fs.SetLogger(slog.NewTextHandler(io.Discard, nil))
	summary := pull.Result{Status: "error"}
	failurePhase := "pull"
	var cleanup error
	defer func() {
		stopProgress()
		result = pull.Failure(result)
		code := pullCode(result)
		switch code {
		case 3:
			summary.Status = "busy"
		case 130:
			summary.Status = "canceled"
		case 1, 2:
			summary.Status = "error"
		}
		if result != nil && code != 4 {
			_, summary.Reason = diagnostic.Fields(result, failurePhase)
			failureLogger := logger.With("action", diagnostic.PullAction(summary.Reason))
			var mismatch *pull.SourceMismatchError
			if errors.As(result, &mismatch) {
				failureLogger = failureLogger.With("existing_source_kind", mismatch.ExistingKind, "requested_source_kind", mismatch.RequestedKind)
			}
			var detail *argumentError
			if errors.As(result, &detail) {
				summary.Reason = "invalid_option"
				failureLogger = configurationLogger(logs, "pull", detail)
				if cleanup == nil {
					logConfigurationError(logs, "pull", result)
				} else {
					diagnostic.LogFinal(failureLogger, "pull failed", "arguments", diagnostic.Wrap("arguments", "invalid_option", result), cleanup)
				}
			} else {
				diagnostic.LogFinal(failureLogger, "pull failed", failurePhase, result, cleanup)
			}
		}
		if summary.Reason == "" {
			summary.Reason = "none"
		}
		summary.CleanupReason = diagnostic.CleanupClass(cleanup)
		if err := writePullSummary(out, p.output, summary, code); err != nil {
			result = &pullOutcome{1, errors.Join(result, err)}
		}
	}()
	defer func() { startup.finish(result) }()
	if p.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}
	ctx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()
	p.config.CancelOnPanic = cancelWork
	p.config.PlanLogger = logger
	openDestination := pull.OpenDestination
	if p.config.DryRun {
		openDestination = pull.OpenReadOnlyDestination
	}
	d, err := openDestination(p.config.Destination)
	if err != nil {
		return err
	}
	defer func() {
		if err := d.Close(); err != nil {
			result = &pullOutcome{1, errors.Join(result, err)}
		}
	}()
	if err := d.BindLayout(p.config.Dirs); err != nil {
		return err
	}
	release := func() error { return nil }
	connections := &pullConnections{workContext: ctx, logger: logger}
	sourceID := "via:" + p.via
	if p.via != "" {
		connections.dialer = viaDialer{endpoint: p.via}
	} else {
		var trace *tracerecord.Writer
		if o.trace != "" {
			trace, err = openTrace(o.trace, mappings{{0, 445}})
			if err != nil {
				return &argumentError{Option: "--trace", Rule: "use a new writable trace file"}
			}
		}
		if trace != nil {
			defer trace.Close()
		}
		keys := startup.openKeys(o.keylog, openKeys)
		s, err := newSession(ctx, proxy.Config{Serial: o.serial, Model: o.model, StateDir: o.stateDir, HoldOnDemand: true, BusyWait: p.busyWait, Logger: logger, Trace: trace, KeyLog: keys})
		if err != nil {
			return err
		}
		released := false
		release = func() error {
			if released {
				return nil
			}
			released = true
			outcome := s.Close()
			cleanup = diagnostic.CleanupError(outcome)
			if cleanup != nil {
				// Cleanup status is independent of any primary retained by Close.
				return outcome
			}
			// Close retains the owner outcome. Ignore only the pure expected
			// cancellation/busy outcome, never its independent joined siblings.
			_, reason := diagnostic.Fields(outcome, "release")
			if outcome != nil && outcome != session.ErrBusy && reason != "canceled" {
				return outcome
			}
			return nil
		}
		defer func() { result = errors.Join(result, release()) }()
		identity, ok := s.(interface{ SourceID() string })
		if !ok {
			return errors.New("session lacks source identity")
		}
		sourceID = identity.SourceID()
		connections.dialer = s
	}
	if p.rebind {
		err = d.RebindSource(sourceID)
	} else {
		err = d.BindSource(sourceID)
	}
	if err != nil {
		return err
	}
	startup.output.resolve(true)
	startup.acceptKeys()
	ctx = pull.Configure(ctx, p.config)
	if p.progress {
		ctx, p.config.Progress = pull.NewProgress(ctx)
		stopProgress = progressOutput.start(p.config.Progress)
	}
	accounting.TokenBucket.SetBwLimit(fs.BwPair{Tx: fs.SizeSuffix(p.config.BytesPerSecond), Rx: fs.SizeSuffix(p.config.BytesPerSecond)})
	defer accounting.TokenBucket.SetBwLimit(fs.BwPair{})
	defer connections.close()
	var source fs.Fs
	err = pull.Operation(logger, func() error {
		var openErr error
		ctx, source, openErr = openSource(ctx, connections.dial)
		return openErr
	})
	if err != nil {
		return err
	}
	// Pool shutdown joins SMB connections before releasing the owned session.
	shutdown := func() error {
		err := connections.shutdown(source)
		if err != nil && result == nil {
			failurePhase = "smb_shutdown"
		}
		return err
	}
	stopped := false
	defer func() {
		if !stopped {
			result = errors.Join(result, shutdown())
		}
	}()
	batch, err := d.Run(ctx, p.config, started, source)
	if batch != nil {
		summary = batch.Result
	}
	if err != nil {
		return err
	}
	stopped = true
	failurePhase = "smb_shutdown"
	if err = connections.shutdownAfterWork(source); err != nil {
		return err
	}
	failurePhase = "release"
	if err = release(); err != nil {
		return err
	}
	failurePhase = "pull_commit"
	if err = batch.Commit(ctx); err != nil {
		return err
	}
	if summary.Status == "nothing-new" && p.config.ErrorOnEmpty {
		return &pullOutcome{4, nil}
	}
	return nil
}

func logPullArgumentError(logs io.Writer, err error) { logConfigurationError(logs, "pull", err) }

// Keep the public stdout schema separate from the engine and its checkpoint
// types. Zero bounds use the same unbounded/unknown spelling as text output.
type pullSummary struct {
	Planned       int    `json:"planned"`
	Status        string `json:"status"`
	Copied        int    `json:"copied"`
	Verified      int    `json:"verified"`
	Deferred      int    `json:"deferred"`
	Bytes         int64  `json:"bytes"`
	FirstScan     bool   `json:"first_scan"`
	Lower         string `json:"lower"`
	Upper         string `json:"upper"`
	Reason        string `json:"reason"`
	CleanupReason string `json:"cleanup_reason"`
	ExitCode      int    `json:"exit_code"`
}

func writePullSummary(out io.Writer, format string, s pull.Result, code int) error {
	lower, upper := s.Bounds.Lower.Format(time.RFC3339Nano), s.Bounds.Upper.Format(time.RFC3339Nano)
	if format == "json" {
		// Streaming encoding retains the summary newline and prior HTML/JS escapes.
		return json.MarshalEncode(jsontext.NewEncoder(out, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true)), pullSummary{
			Status: s.Status, Planned: s.Planned, Copied: s.Copied, Verified: s.Verified, Deferred: s.Deferred,
			Bytes: s.Bytes, FirstScan: s.FirstScan, Lower: lower, Upper: upper,
			Reason: s.Reason, CleanupReason: s.CleanupReason, ExitCode: code,
		})
	}
	if s.Status == "planned" {
		_, err := fmt.Fprintf(out, "status=planned planned=%d copied=0 verified=0 deferred=%d bytes=%d first_scan=%t lower=%s upper=%s reason=%s cleanup_reason=%s\n", s.Planned, s.Deferred, s.Bytes, s.FirstScan, lower, upper, s.Reason, s.CleanupReason)
		return err
	}
	_, err := fmt.Fprintf(out, "status=%s copied=%d verified=%d deferred=%d bytes=%d first_scan=%t lower=%s upper=%s reason=%s cleanup_reason=%s\n", s.Status, s.Copied, s.Verified, s.Deferred, s.Bytes, s.FirstScan, lower, upper, s.Reason, s.CleanupReason)
	return err
}
