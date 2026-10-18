package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-json-experiment/json"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestMainExitCodeBeforeBootstrap(t *testing.T) {
	discoveryOption, discoveryRule := "--bind", "LAN discovery requires a literal LAN IPv4 address or 0.0.0.0"
	windows := runtime.GOOS == "windows"
	if windows {
		discoveryOption, discoveryRule = "--lan-discovery", "LAN discovery is unsupported on Windows"
	}
	for _, tc := range []struct {
		name, option, rule string
		args               []string
		accepted           bool
	}{
		{"default bind", discoveryOption, discoveryRule, []string{"--lan-discovery"}, false},
		{"private bind and captures", discoveryOption, discoveryRule, []string{"--lan-discovery", "--bind=PRIVATE_MARKER", "--trace=PRIVATE_MARKER", "--keylog=PRIVATE_MARKER"}, false},
		{"earlier validation", "--idle", "duration must be nonnegative", []string{"--lan-discovery", "--idle=-1s"}, false},
		{"earlier syntax", "--idle", "use a duration such as 2m or 0", []string{"--lan-discovery", "--idle=PRIVATE_MARKER"}, false},
		{"positional", "arguments", "positional arguments are not allowed", []string{"--lan-discovery", "PRIVATE_MARKER"}, false},
		{"boolean syntax", "--lan-discovery", "use a boolean value", []string{"--lan-discovery=PRIVATE_MARKER"}, false},
		{"version", "", "", []string{"--version"}, true},
		{"false boolean", "", "", []string{"--version", "--lan-discovery=false"}, true},
		{"last boolean wins", "", "", []string{"--version", "--lan-discovery", "--lan-discovery=false"}, true},
		{"wildcard bind and version", "--lan-discovery", "LAN discovery is unsupported on Windows", []string{"--version", "--lan-discovery", "--bind=0.0.0.0", "--trace=PRIVATE_MARKER", "--keylog=PRIVATE_MARKER"}, !windows},
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
			if code != 2 || out.Len() != 0 || record.Phase != "arguments" || record.Reason != "invalid_option" || record.Option != tc.option || record.Rule != tc.rule || record.Help != "seestar-wormhole proxy --help" {
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
		{}, {"--help"}, {"proxy", "--help"},
		{"unknown"}, {"--unknown"}, {"proxy", "--unknown"}, {"proxy", "--idle=bad"},
		{"proxy", "--idle=-1s"}, {"proxy", "extra"},
	} {
		for _, unsupported := range []bool{false, true} {
			var out, logs bytes.Buffer
			support := func() error {
				if unsupported {
					return errors.New("unsupported platform")
				}
				return nil
			}
			code := mainExitCodeWithPlatform(args, &out, &logs,
				func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
				func() (io.Writer, func()) { return &logs, func() {} }, support)
			want := 2
			if len(args) == 0 || args[len(args)-1] == "--help" {
				want = 0
			}
			if code != want {
				t.Errorf("%q unsupported=%t: exit %d want %d; logs=%s", args, unsupported, code, want, &logs)
			}
		}
	}
}
