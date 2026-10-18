package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// injected constructor blocks any accidental cloud or device acquisition.
func TestE2EHelpDoesNotOpenSession(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"help", "proxy"}, {"__completeNoDesc", "proxy", ""}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, logs bytes.Buffer
			opened := 0
			err := runWithSession(t.Context(), args, &out, &logs, func(context.Context, proxy.Config) (commandSession, error) {
				opened++
				return nil, errors.New("blocked informational session")
			})
			if opened != 0 || commandExitCode(options{command: commandProxy}, err) != 0 || out.Len() == 0 || logs.Len() != 0 {
				t.Fatalf("informational dispatch: opened=%d code=%d stdout=%q logs=%q", opened, commandExitCode(options{command: commandProxy}, err), &out, &logs)
			}
		})
	}
}

// Keep the proxy's literal default listeners disabled: these tests exercise the
// real bootstrap and runtime dispatch without opening sockets for forwarding.
func dispatchProxyArgs() []string {
	return []string{"--port=0:4700", "--port=0:445", "--port=0:80", "--port=0:4350", "--port=0:4554", "--port=0:4555", "--port=0:4801", "--port=0:4800", "--port=0:4804", "--port=0:4361"}
}

type rootFlagPermutation struct {
	name          string
	before, after []string
}

func dispatchPermutations() []rootFlagPermutation {
	return []rootFlagPermutation{
		{name: "plain"},
		{name: "leading version false", before: []string{"--version=false"}},
		{name: "leading help false", before: []string{"--help=false"}},
		{name: "leading short help false", before: []string{"-h=false"}},
		{name: "repeated root flag", before: []string{"--version=true", "--version=false"}},
		{name: "override after command", before: []string{"--version=true", "--help=false"}, after: []string{"--version=false"}},
		{name: "flags after command", after: []string{"--version=false", "--help=false"}},
	}
}

func TestDispatchRuntimeAndExitPolicy(t *testing.T) {
	for _, permutation := range dispatchPermutations() {
		for _, cause := range []error{session.ErrBusy, context.Canceled, cloud.ErrConfig} {
			var out, logs bytes.Buffer
			args := append([]string{}, permutation.before...)
			args = append(args, "proxy")
			args = append(args, permutation.after...)
			args = append(args, dispatchProxyArgs()...)
			args = append(args, "--version=false")
			opened := 0
			code := mainExitCodeWithRunner(args, &out, &logs,
				func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
				func() (io.Writer, func()) { return &logs, func() {} }, func() error { return nil },
				func(ctx context.Context, o options, stdout, stderr io.Writer) error {
					return runParsedWithListener(ctx, o, stdout, stderr, func(context.Context, proxy.Config) (commandSession, error) { opened++; return nil, cause },
						func(string, string) (net.Listener, error) { t.Fatal("unexpected listener"); return nil, nil })
				})
			want := 1
			if cause == cloud.ErrConfig {
				want = 2
			}
			if code != want || opened != 1 || out.Len() != 0 {
				t.Fatalf("exit=%d opened=%d stdout=%q logs=%q", code, opened, &out, &logs)
			}
		}
	}
}

func TestDispatchInvalidPortRefusesBeforeOwnerStarts(t *testing.T) {
	var out, logs bytes.Buffer
	runtimeCalls, ownerStarts := 0, 0
	code := mainExitCodeWithRunner([]string{"proxy", "--port=invalid"}, &out, &logs,
		func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
		func() (io.Writer, func()) { return &logs, func() {} },
		func() error { return nil },
		func(ctx context.Context, o options, stdout, stderr io.Writer) error {
			runtimeCalls++
			return runParsedWithListener(ctx, o, stdout, stderr,
				func(context.Context, proxy.Config) (commandSession, error) {
					ownerStarts++
					return nil, errors.New("unexpected owner start")
				},
				func(string, string) (net.Listener, error) { t.Fatal("unexpected listener"); return nil, nil })
		})
	if code != 2 || out.Len() != 0 || runtimeCalls != 0 || ownerStarts != 0 {
		t.Fatalf("exit=%d runtime=%d owners=%d stdout=%q logs=%q; want exit 2 before runtime or owner start", code, runtimeCalls, ownerStarts, &out, &logs)
	}
}

func TestDispatchInformationalOutcomes(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--help"}, {"-h"}, {"--version"}, {"--version=false"},
		{"help"}, {"help", "proxy"}, {"help", "help"}, {"help", "unknown-topic"},
		{"proxy", "--help"}, {"help", "--help"},
		{"--help", "proxy"},
		{"proxy", "--version"}, {"--version", "proxy"},
		{"--version=false", "proxy", "--version"},
		{"--version", "proxy", "--help"}, // Help takes precedence over version.
		{"__complete", ""}, {"__completeNoDesc", "proxy", "--"},
	} {
		for _, unsupported := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/unsupported=%t", args, unsupported), func(t *testing.T) {
				var out, logs bytes.Buffer
				code := mainExitCodeWithRunner(args, &out, &logs,
					func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
						t.Fatal("information acquired signal handles")
						return nil, nil
					},
					func() (io.Writer, func()) { t.Fatal("information acquired log handles"); return nil, nil },
					func() error {
						if unsupported {
							return errors.New("unsupported")
						}
						return nil
					},
					func(context.Context, options, io.Writer, io.Writer) error {
						t.Fatal("information entered runtime")
						return nil
					})
				if code != 0 || out.Len() == 0 || logs.Len() != 0 {
					t.Fatalf("exit=%d stdout=%q logs=%q, want 0 and information only", code, &out, &logs)
				}
			})
		}
	}
}

func TestDispatchUsageErrors(t *testing.T) {
	for _, permutation := range dispatchPermutations() {
		for _, subcommand := range []string{"proxy", "help", "__complete"} {
			// Completion treats supplied flags as its command-line payload. Only an
			// empty request is a Cobra usage error (MinimumNArgs).
			if subcommand == "__complete" && permutation.name != "plain" {
				continue
			}
			for _, unsupported := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/unsupported=%t", permutation.name, subcommand, unsupported), func(t *testing.T) {
					var out, logs bytes.Buffer
					args := append([]string{}, permutation.before...)
					args = append(args, subcommand)
					args = append(args, permutation.after...)
					if subcommand != "__complete" {
						args = append(args, "--unknown")
					}
					code := mainExitCodeWithRunner(args, &out, &logs,
						func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
							if unsupported {
								t.Fatal("refusal acquired Windows signals")
							}
							return ctx, func() {}
						},
						func() (io.Writer, func()) {
							if unsupported {
								t.Fatal("refusal acquired Windows log handles")
							}
							return &logs, func() {}
						},
						func() error {
							if unsupported {
								return errors.New("unsupported")
							}
							return nil
						},
						func(context.Context, options, io.Writer, io.Writer) error {
							t.Fatal("usage error entered runtime")
							return nil
						})
					if code != 2 || out.Len() != 0 || logs.Len() == 0 {
						t.Fatalf("exit=%d stdout=%q logs=%q; want 2, no summary, diagnostic", code, &out, &logs)
					}
					if subcommand == "proxy" {
						if !strings.Contains(logs.String(), "seestar-wormhole "+subcommand+" --help") {
							t.Fatalf("wrong command diagnostic: %q", &logs)
						}
					}
				})
			}
		}
	}
}

func TestDispatchParsesOnce(t *testing.T) {
	var out, logs bytes.Buffer
	validations := 0
	code := mainExitCodeWithRunner([]string{"--version", "proxy", "--lan-discovery", "--bind=0.0.0.0"}, &out, &logs,
		func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
			t.Fatal("version acquired signals")
			return nil, nil
		},
		func() (io.Writer, func()) { t.Fatal("version acquired log handles"); return nil, nil },
		func() error { validations++; return nil },
		func(context.Context, options, io.Writer, io.Writer) error {
			t.Fatal("version entered runtime")
			return nil
		})
	if code != 0 || validations != 1 || out.String() != versionString()+"\n" {
		t.Fatalf("exit=%d validations=%d stdout=%q; want 0/1/version", code, validations, &out)
	}
}
