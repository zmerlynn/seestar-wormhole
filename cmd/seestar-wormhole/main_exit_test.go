package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestCommandShutdownSignals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		windows bool
		wantHUP bool
	}{
		{"Unix bare UI", nil, false, true},
		{"Unix UI", []string{"ui"}, false, true},
		{"Unix proxy", []string{"proxy"}, false, false},
		{"Unix discovery", discoveryArgs(), false, false},
		{"Windows bare UI", nil, true, false},
		{"Windows UI", []string{"ui"}, true, false},
		{"Windows proxy", []string{"proxy"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !guiEnabled && (len(tc.args) == 0 || tc.args[0] == "ui") {
				t.Skip("GUI only")
			}
			want := []os.Signal{os.Interrupt, syscall.SIGTERM}
			if tc.wantHUP {
				want = append(want, syscall.SIGHUP)
			}
			stopped, executed := false, false
			code := mainExitCodeWithRunner(tc.args, io.Discard, io.Discard,
				func(ctx context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
					if !slices.Equal(signals, want) {
						t.Errorf("signals = %v, want %v", signals, want)
					}
					ctx, cancel := context.WithCancel(ctx)
					cancel() // Model delivery through the shared signal cancellation path.
					return ctx, func() { stopped = true; cancel() }
				},
				func() (io.Writer, func()) { return io.Discard, func() {} }, tc.windows,
				func(ctx context.Context, _ options, _, _ io.Writer) error {
					executed = true
					if ctx.Err() != context.Canceled {
						t.Error("signal cancellation did not reach the runner")
					}
					return nil
				})
			if code != 0 || !executed || !stopped {
				t.Fatalf("exit=%d executed=%t stopped=%t", code, executed, stopped)
			}
		})
	}
}

func TestMainExitCodeBeforeBootstrap(t *testing.T) {
	discoveryOption, discoveryRule := "--bind", "LAN discovery requires a literal LAN IPv4 address or 0.0.0.0"
	windows := runtime.GOOS == "windows"
	for _, tc := range []struct {
		name, option, rule string
		args               []string
		accepted           bool
	}{
		{"all interfaces without discovery", "--lan-discovery-all-interfaces", "requires --lan-discovery", []string{"--lan-discovery-all-interfaces"}, false},
		{"all interfaces with discovery disabled", "--lan-discovery-all-interfaces", "requires --lan-discovery", []string{"--lan-discovery-all-interfaces", "--lan-discovery=false"}, false},
		{"default bind", discoveryOption, discoveryRule, []string{"--lan-discovery"}, false},
		{"private bind and captures", discoveryOption, discoveryRule, []string{"--lan-discovery", "--bind=PRIVATE_MARKER"}, false},
		{"earlier validation", "--idle", "duration must be nonnegative", []string{"--lan-discovery", "--idle=-1s"}, false},
		{"earlier syntax", "--idle", "use a duration such as 2m or 0", []string{"--lan-discovery", "--idle=PRIVATE_MARKER"}, false},
		{"positional", "arguments", "positional arguments are not allowed", []string{"--lan-discovery", "PRIVATE_MARKER"}, false},
		{"boolean syntax", "--lan-discovery", "use a boolean value", []string{"--lan-discovery=PRIVATE_MARKER"}, false},
		{"version", "", "", []string{"--version"}, true},
		{"false boolean", "", "", []string{"--version", "--lan-discovery=false"}, true},
		{"last boolean wins", "", "", []string{"--version", "--lan-discovery", "--lan-discovery=false"}, true},
		{"wildcard bind and version", "", "", []string{"--version", "--lan-discovery", "--bind=0.0.0.0"}, true},
		{"help", "", "", []string{"--lan-discovery", "--help"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, logs bytes.Buffer
			var notified, logOpened, stopped, logClosed bool
			code := mainExitCodeWithBootstrap(append([]string{"proxy"}, tc.args...), &out, &logs,
				func(ctx context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
					notified = true
					if len(signals) != 2 || signals[0] != os.Interrupt || signals[1] != syscall.SIGTERM {
						t.Errorf("signals = %v, want Interrupt and SIGTERM", signals)
					}
					return ctx, func() { stopped = true }
				}, func() (io.Writer, func()) {
					logOpened = true
					return &logs, func() { logClosed = true }
				})
			bootstrap := !windows && !tc.accepted && tc.name != "help"
			if notified != bootstrap || logOpened != bootstrap || stopped != bootstrap || logClosed != bootstrap {
				t.Errorf("bootstrap: notify=%t log=%t stop=%t close=%t; want all %t", notified, logOpened, stopped, logClosed, bootstrap)
			}
			if tc.accepted {
				if code != 0 || out.String() != versionString()+"\n" || logs.Len() != 0 {
					t.Fatalf("version: code=%d out=%q logs=%q", code, &out, &logs)
				}
				return
			}
			if tc.name == "help" {
				if code != 0 || !strings.Contains(out.String(), "--lan-discovery") || logs.Len() != 0 {
					t.Fatalf("help: code=%d out=%q logs=%q", code, &out, &logs)
				}
				return
			}
			var record struct{ Phase, Reason, Option, Rule, Help string }
			if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatalf("want one JSON diagnostic, got %q: %v", &logs, err)
			}
			if code != 2 || out.Len() != 0 || record.Phase != "arguments" || record.Reason != "invalid_option" || record.Option != tc.option || record.Rule != tc.rule || record.Help != programName+" proxy --help" {
				t.Fatalf("refusal: code=%d out=%q diagnostic=%+v; want arguments/invalid_option, option=%q rule=%q and help hint", code, &out, record, tc.option, tc.rule)
			}
			if strings.Contains(logs.String(), "PRIVATE_MARKER") {
				t.Fatalf("diagnostic leaked option value: %q", &logs)
			}
		})
	}
}

func TestUsageExitAcrossCommands(t *testing.T) {
	for _, args := range [][]string{
		{"--help"}, {"proxy", "--help"},
		{"unknown"}, {"--unknown"}, {"proxy", "--unknown"}, {"proxy", "--idle=bad"},
		{"proxy", "--idle=-1s"}, {"proxy", "extra"},
	} {
		for _, windows := range []bool{false, true} {
			var out, logs bytes.Buffer
			code := mainExitCodeWithPlatform(args, &out, &logs,
				func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
				func() (io.Writer, func()) { return &logs, func() {} }, windows)
			want := 2
			if args[len(args)-1] == "--help" {
				want = 0
			}
			if code != want {
				t.Errorf("%q windows=%t: exit %d want %d; logs=%s", args, windows, code, want, &logs)
			}
		}
	}
}

// failedOutput refuses every write, as a full disk or closed pipe does.
type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, syscall.EIO }

// Help that cannot be written exits 1, as a failed version write does, and
// an unknown help topic is a usage error, as in seestar-copy-cli.
func TestHelpOutcomes(t *testing.T) {
	bootstrap := func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} }
	for _, args := range [][]string{{"--help"}, {"help"}, {"help", "proxy"}, {"proxy", "--help"}, {"--version"}} {
		for _, windows := range []bool{false, true} {
			var logs bytes.Buffer
			code := mainExitCodeWithPlatform(args, failedOutput{}, &logs, bootstrap,
				func() (io.Writer, func()) { return &logs, func() {} }, windows)
			if code != 1 {
				t.Errorf("%q to a failed output, windows=%t: exit %d, want 1", args, windows, code)
			}
		}
	}
	for _, args := range [][]string{{"help", "proxy"}, {"help", "licenses"}} {
		var out, logs bytes.Buffer
		code := mainExitCodeWithPlatform(args, &out, &logs, bootstrap, func() (io.Writer, func()) { return &logs, func() {} }, false)
		if code != 0 || !strings.Contains(out.String(), programName+" "+args[1]) || !strings.Contains(out.String(), "-h, --help") || logs.Len() != 0 {
			t.Errorf("%q: exit %d out=%q logs=%q", args, code, &out, &logs)
		}
	}
	for _, args := range [][]string{{"help", "nonesuch"}, {"help", "proxy", "nonesuch"}} {
		for _, windows := range []bool{false, true} {
			var out, logs bytes.Buffer
			code := mainExitCodeWithPlatform(args, &out, &logs, bootstrap,
				func() (io.Writer, func()) { return &logs, func() {} }, windows)
			var record struct{ Msg, Phase, Reason, Option, Rule, Help string }
			if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatalf("%q: want one JSON diagnostic, got %q: %v", args, &logs, err)
			}
			if code != 2 || out.Len() != 0 || record.Msg != "help failed" || record.Phase != "arguments" || record.Reason != "invalid_option" ||
				record.Option != "help" || record.Rule != "unknown help topic; give a command name, such as sync" || record.Help != programName+" help --help" {
				t.Errorf("%q windows=%t: exit %d out=%q diagnostic=%+v", args, windows, code, &out, record)
			}
		}
	}
}
