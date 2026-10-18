package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Ported from review/e2e-pull's M1 constructor probes. Loopback only; the
// injected constructor blocks any accidental cloud or device acquisition.
func TestE2EPullDispatch(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	opened := 0
	err = runWithSession(t.Context(), []string{"--version=false", "pull", "--via=" + endpoint, "--output=json", t.TempDir()}, &out, &logs, keylog.Open,
		func(_ context.Context, cfg proxy.Config) (commandSession, error) {
			opened++
			t.Errorf("unexpected owned session: HoldOnDemand=%t", cfg.HoldOnDemand)
			return nil, errors.New("blocked unintended session")
		})
	var summary struct {
		Status   string `json:"status"`
		ExitCode int    `json:"exit_code"`
	}
	if decodeErr := json.Unmarshal(out.Bytes(), &summary, json.MatchCaseInsensitiveNames(true)); decodeErr != nil {
		t.Fatalf("pull summary: %v; stdout=%q logs=%q", decodeErr, &out, &logs)
	}
	if opened != 0 || pullCode(err) != 1 || summary.Status != "error" || summary.ExitCode != 1 || !strings.Contains(logs.String(), "pull failed") {
		t.Fatalf("via dispatch: opened=%d code=%d summary=%+v logs=%q", opened, pullCode(err), summary, &logs)
	}
}

func TestE2EHelpDoesNotOpenSession(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"help", "pull"}, {"help", "proxy"}, {"__complete", "pull", ""}, {"__completeNoDesc", "proxy", ""}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, logs bytes.Buffer
			opened := 0
			err := runWithSession(t.Context(), args, &out, &logs, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) {
				opened++
				return nil, errors.New("blocked informational session")
			})
			if opened != 0 || pullCode(err) != 0 || out.Len() == 0 || logs.Len() != 0 {
				t.Fatalf("informational dispatch: opened=%d code=%d stdout=%q logs=%q", opened, pullCode(err), &out, &logs)
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
		for _, mode := range []string{"proxy", "direct", "via"} {
			for _, cause := range []struct {
				name     string
				err      error
				pullExit int
			}{
				{"busy", session.ErrBusy, 3}, {"canceled", context.Canceled, 130}, {"configuration", cloud.ErrConfig, 2},
			} {
				// Via has no owned constructor on which to inject busy/config failures.
				if mode == "via" && cause.err != context.Canceled {
					continue
				}
				for _, unsupported := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/unsupported=%t", permutation.name, mode, cause.name, unsupported), func(t *testing.T) {
						var out, logs bytes.Buffer
						ctx := t.Context()
						command := "pull"
						flags := []string{"--output=json"}
						wantCode, wantOpened := cause.pullExit, 1
						fixture := &pullSessionFixture{dialErr: cause.err}
						if mode == "proxy" {
							command = "proxy"
							flags = dispatchProxyArgs()
							wantCode = 1
							if cause.err == cloud.ErrConfig {
								wantCode = 2
							}
						} else if mode == "via" {
							flags = append(flags, "--via=127.0.0.1:1")
							wantOpened = 0
							var cancel context.CancelFunc
							ctx, cancel = context.WithCancel(ctx)
							cancel() // Refuse locally before any SMB network I/O.
						}
						args := append([]string{}, permutation.before...)
						args = append(args, command)
						args = append(args, permutation.after...)
						flags = append(flags, "--version=false")
						if mode != "proxy" {
							flags = append(flags, "--", t.TempDir())
						}
						args = append(args, flags...)
						opened, stopped, logClosed := 0, 0, 0
						code := mainExitCodeWithRunner(args, &out, &logs,
							func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
								return ctx, func() { stopped++ }
							},
							func() (io.Writer, func()) { return &logs, func() { logClosed++ } },
							func() error {
								if unsupported {
									return errors.New("unsupported")
								}
								return nil
							},
							func(ctx context.Context, o options, stdout, stderr io.Writer) error {
								return runParsedWithListener(ctx, o, stdout, stderr,
									func(string, *slog.Logger) (*keylog.Writer, error) { t.Fatal("unexpected capture"); return nil, nil },
									func(_ context.Context, cfg proxy.Config) (commandSession, error) {
										opened++
										if cfg.HoldOnDemand != (mode == "direct") {
											t.Errorf("HoldOnDemand=%t, want %t", cfg.HoldOnDemand, mode == "direct")
										}
										if mode == "proxy" || cause.err == cloud.ErrConfig {
											return nil, cause.err
										}
										return fixture, nil
									},
									func(string, string) (net.Listener, error) { t.Fatal("unexpected proxy listener"); return nil, nil })
							})
						wantClosed := 0
						if mode == "direct" && cause.err != cloud.ErrConfig {
							wantClosed = 1
						}
						if code != wantCode || opened != wantOpened || fixture.closed != wantClosed || stopped != 1 || logClosed != 1 {
							t.Fatalf("exit=%d owners=%d releases=%d signal stops=%d log closes=%d; want %d/%d/%d/1/1; stdout=%q logs=%q", code, opened, fixture.closed, stopped, logClosed, wantCode, wantOpened, wantClosed, &out, &logs)
						}
						if mode == "proxy" {
							if out.Len() != 0 || (cause.err != context.Canceled && !strings.Contains(logs.String(), "proxy failed")) {
								t.Fatalf("proxy output: stdout=%q logs=%q", &out, &logs)
							}
						} else {
							status, reason := "error", "operation_failed"
							if cause.err == session.ErrBusy {
								status, reason = "busy", "busy"
							}
							if cause.err == context.Canceled {
								status, reason = "canceled", "canceled"
							}
							if cause.err == cloud.ErrConfig {
								reason = "invalid_configuration"
							}
							assertJSONSummary(t, out.String(), code, status, reason, "none")
							if cause.err != context.Canceled && !strings.Contains(logs.String(), "pull failed") {
								t.Fatalf("pull diagnostic missing: %q", &logs)
							}
						}
					})
				}
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
				keylog.Open,
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
		{"help"}, {"help", "proxy"}, {"help", "pull"}, {"help", "help"}, {"help", "unknown-topic"},
		{"proxy", "--help"}, {"pull", "-h"}, {"help", "--help"},
		{"--help", "proxy"}, {"--help", "pull"}, {"--version=false", "pull", "--help"},
		{"proxy", "--version"}, {"pull", "--version"}, {"--version", "proxy"}, {"--version", "pull"},
		{"--version=false", "pull", "--version"}, {"--version=false", "proxy", "--version"},
		{"--version", "pull", "--help"}, // Help takes precedence over version.
		{"__complete", ""}, {"__complete", "pull", "--"}, {"__completeNoDesc", "proxy", "--"},
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
		for _, subcommand := range []string{"proxy", "pull", "help", "__complete"} {
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
					if subcommand == "proxy" || subcommand == "pull" {
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
