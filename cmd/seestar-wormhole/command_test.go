package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-json-experiment/json"
	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBareInvocationHelp(t *testing.T) {
	var out, logs bytes.Buffer
	err := runWithListener(context.Background(), nil, &out, &logs, idleCommandFixture, func(string, string) (net.Listener, error) {
		t.Error("bare invocation opened a listener")
		return nil, errors.New("unexpected listener")
	})
	if !errors.Is(err, errHelp) || logs.Len() != 0 || !strings.Contains(out.String(), "Usage:") || !strings.Contains(out.String(), "proxy") {
		t.Fatalf("bare invocation: err=%v stdout=%q stderr=%q", err, &out, &logs)
	}
}

func TestRootVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"--version"}, &out, io.Discard); err != nil || !strings.Contains(out.String(), "seestar-wormhole ") {
		t.Fatalf("version: err=%v stdout=%q", err, &out)
	}
}

func TestCommandTreeAndFlagScope(t *testing.T) {
	var o options
	root := newCommand(&o, io.Discard, func() error { return nil })
	commands := root.Commands()
	if len(commands) != 2 || commands[0].Name() != "licenses" || commands[1].Name() != "proxy" || len(commands[1].Aliases) != 0 {
		t.Fatalf("commands = %v, want licenses and proxy with no aliases", commands)
	}
	if root.Flags().Lookup("version") == nil || root.PersistentFlags().NFlag() != 0 {
		t.Fatal("root version or flag scope changed")
	}
	for _, name := range []string{"bind", "port", "serial", "state-dir", "idle", "pause-threshold", "log-format", "log-level", "lan-discovery"} {
		if root.Flags().Lookup(name) != nil || root.PersistentFlags().Lookup(name) != nil || commands[1].Flags().Lookup(name) == nil {
			t.Errorf("%s must be proxy-local", name)
		}
	}
	commands[1].Flags().VisitAll(func(f *pflag.Flag) {
		if f.Shorthand != "" && (f.Name != "port" || f.Shorthand != "p") {
			t.Errorf("unexpected shorthand %q for %s", f.Shorthand, f.Name)
		}
	})
}

func TestShellRefusalsWithoutResources(t *testing.T) {
	for _, args := range [][]string{
		{"serve"}, {"pull"}, {"PRIVATE_COMMAND"}, {"--bind", "PRIVATE_VALUE"},
		{"--version", "--log-format", "text"}, {"--version=PRIVATE_VALUE"},
		{"proxy", "-idle", "2m"}, {"proxy", "-log-format", "text"}, {"proxy", "-help"},
		{"proxy", "--version", "--idle=-1s"}, {"proxy", "--version", "PRIVATE_VALUE"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, logs bytes.Buffer
			err := runWithListener(context.Background(), args, &out, &logs, func(context.Context, proxy.Config) (commandSession, error) {
				t.Fatal("invalid invocation created a session")
				return nil, nil
			}, func(string, string) (net.Listener, error) {
				t.Fatal("invalid invocation opened listeners")
				return nil, nil
			})
			if err == nil || out.Len() != 0 || logs.Len() == 0 || strings.Contains(logs.String(), "PRIVATE_") || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatalf("refusal: err=%v stdout=%q stderr=%q", err, &out, &logs)
			}
		})
	}
}

func TestCobraPortSpellingsAndBooleanRules(t *testing.T) {
	for _, port := range []string{"--port", "-p"} {
		o, err := parse([]string{"proxy", port, "1445:445", "--lan-discovery=false", "--version", "--idle=2m"}, io.Discard)
		if err != nil || o.ports[1].local != 1445 || o.lanDiscovery || !o.version || o.idle != 2*time.Minute {
			t.Fatalf("%s: options=%+v err=%v", port, o, err)
		}
	}
}

// Exercise the actual production bootstrap branch on Linux too. Removing the
// Windows preflight must invoke the fatal resource hooks and fail this test.
func TestWindowsPreflightOrdering(t *testing.T) {
	unsupported := func() error { return errors.New("unsupported platform") }
	for _, args := range [][]string{
		{"proxy", "--lan-discovery"},
		{"proxy", "--lan-discovery", "--bind=0.0.0.0"},
		{"proxy", "--version", "--lan-discovery", "--bind=0.0.0.0"},
	} {
		var out, logs bytes.Buffer
		code := mainExitCodeWithPlatform(args, &out, &logs, func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
			t.Fatal("signal bootstrap ran before Windows refusal")
			return nil, nil
		}, func() (io.Writer, func()) {
			t.Fatal("log bootstrap ran before Windows refusal")
			return nil, nil
		}, unsupported)
		var record struct{ Option, Rule, Phase, Reason string }
		if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err, &logs)
		}
		if code != 2 || out.Len() != 0 || record.Option != "--lan-discovery" || record.Rule != "LAN discovery is unsupported on Windows" || record.Phase != "arguments" || record.Reason != "invalid_option" || strings.Contains(logs.String(), "PRIVATE_CAPTURE") {
			t.Fatalf("Windows preflight: exit=%d out=%q diagnostic=%+v", code, &out, record)
		}
	}
}

func TestRootHelpExitCode(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"proxy", "--help"}} {
		var out, logs bytes.Buffer
		code := mainExitCodeWithBootstrap(args, &out, &logs,
			func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
			func() (io.Writer, func()) { return &logs, func() {} })
		if code != 0 || !strings.Contains(out.String(), "Usage:") || logs.Len() != 0 {
			t.Fatalf("args=%v exit=%d out=%q logs=%q", args, code, &out, &logs)
		}
	}
}
