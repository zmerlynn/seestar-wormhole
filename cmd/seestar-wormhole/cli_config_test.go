package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-json-experiment/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/rclone/rclone/fs"
	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Compare the complete record, allowing only slog's timestamp to vary.
func assertConfigRecord(t *testing.T, logs []byte, command, option, rule string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(logs, &got, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatalf("want exactly one JSON diagnostic: %s: %v", logs, err)
	}
	if _, ok := got["time"].(string); !ok {
		t.Fatalf("missing timestamp: %v", got)
	}
	delete(got, "time")
	want := map[string]any{"level": "ERROR", "msg": command + " failed", "option": option, "rule": rule, "help": "seestar-wormhole " + command + " --help", "phase": "arguments", "reason": "invalid_option"}
	if command == "proxy" {
		want["cleanup_reason"] = "none"
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic=%v want=%v", got, want)
	}
}

func TestCLIConfigAttribution(t *testing.T) {
	for _, command := range []string{"proxy", "pull"} {
		for _, format := range []string{"text", "json"} {
			for _, capture := range []string{"none", "open-failure", "worker-failure"} {
				for _, problem := range []string{"email", "password", "state", "selection", "trace"} {
					t.Run(command+"/"+format+"/"+capture+"/"+problem, func(t *testing.T) {
						synctest.Test(t, func(t *testing.T) {
							t.Setenv("SEESTAR_EMAIL", "test@example.invalid")
							t.Setenv("SEESTAR_PASSWORD", "PRIVATE_PASSWORD")
							args := []string{command, "--log-format=" + format, "--log-level=debug"}
							if command == "pull" {
								args = append(args, "--output="+format)
							}
							if capture != "none" {
								args = append(args, "--keylog=PRIVATE_KEYLOG_PATH")
							}
							option, rule := "SEESTAR_EMAIL", "use a valid UTF-8 email of at most 4096 bytes matching the supported account format"
							switch problem {
							case "email":
								t.Setenv("SEESTAR_EMAIL", "")
							case "password":
								t.Setenv("SEESTAR_PASSWORD", "bad")
								option, rule = "SEESTAR_PASSWORD", "use a valid UTF-8 password of 8..32 UTF-16 units"
							case "state":
								state := filepath.Join(t.TempDir(), "PRIVATE_STATE_PATH")
								if err := os.WriteFile(state, []byte("block directory"), 0600); err != nil {
									t.Fatal(err)
								}
								args = append(args, "--state-dir="+state)
								option, rule = "--state-dir", "use a private writable state directory and installation identity file"
							case "selection":
								option, rule = "--serial", "select an existing device from the account catalog"
							case "trace":
								args = append(args, "--trace="+t.TempDir())
								option, rule = "--trace", "use a new writable trace file"
							}
							if command == "pull" {
								args = append(args, t.TempDir())
							}
							var out, logs bytes.Buffer
							open := keylog.Open
							if capture == "open-failure" {
								open = func(string, *slog.Logger) (*keylog.Writer, error) { return nil, errors.New("PRIVATE_KEYLOG_PATH") }
							}
							if capture == "worker-failure" {
								open = func(_ string, logger *slog.Logger) (*keylog.Writer, error) {
									keys := keylog.New(failedStartupKeys{}, logger)
									keys.Write([]byte("PRIVATE_PAYLOAD\n"))
									synctest.Wait()
									return keys, nil
								}
							}
							factory := func(ctx context.Context, cfg proxy.Config) (commandSession, error) {
								if problem == "selection" {
									// A preliminary dependency record must not survive a typed refusal.
									cfg.Logger.Debug("dependency_startup", "count", 1)
									return nil, diagnostic.Wrap("device_selection", "invalid_configuration", errors.Join(cloud.ErrConfig, &argumentError{Option: option, Rule: rule}))
								}
								return proxy.New(ctx, cfg)
							}
							// Ephemeral loopback listeners avoid collisions and never reach a device:
							// each real construction fails local credential/state validation.
							err := runWithListener(t.Context(), args, &out, &logs, open, factory, func(_, _ string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") })
							if got := pullCode(err); got != 2 {
								t.Fatalf("exit=%d want=2 err=%v logs=%s", got, err, &logs)
							}
							assertConfigRecord(t, logs.Bytes(), command, option, rule)
							assertNoSensitiveErrorDetails(t, err, "PRIVATE_PASSWORD", "PRIVATE_STATE_PATH", "PRIVATE_KEYLOG_PATH", "PRIVATE_PAYLOAD")
							if command == "pull" {
								if format == "json" {
									assertJSONSummary(t, out.String(), 2, "error", "invalid_option", "none")
								} else {
									const want = "status=error copied=0 verified=0 deferred=0 bytes=0 first_scan=false lower=0001-01-01T00:00:00Z upper=0001-01-01T00:00:00Z reason=invalid_option cleanup_reason=none\n"
									if out.String() != want {
										t.Fatalf("text summary=%q want=%q", out.String(), want)
									}
								}
							} else if out.Len() != 0 {
								t.Fatalf("proxy stdout=%s", &out)
							}
						})
					})
				}
			}
		}
	}
}

func TestCLIParserRules(t *testing.T) {
	var o options
	root := newCommand(&o, io.Discard, func() error { return nil })
	commands := append(root.Commands(), root)
	for _, cmd := range commands {
		cmd.InitDefaultHelpFlag()
		cmd.Flags().VisitAll(func(flag *pflag.Flag) {
			prefix := []string{}
			if cmd != root {
				prefix = append(prefix, cmd.Name())
			}
			// All value-bearing flags must attribute a missing value, even string and
			// string-array flags that intentionally accept arbitrary parser input.
			if flag.Value.Type() != "bool" {
				t.Run(cmd.Name()+"/"+flag.Name+"/missing", func(t *testing.T) {
					_, err := parse(append(prefix, "--"+flag.Name), io.Discard)
					var detail *argumentError
					if !errors.As(err, &detail) || detail.Option != "--"+flag.Name || detail.Rule != "requires a value" {
						t.Fatalf("missing value: %v", err)
					}
				})
			}
			rule := ""
			switch flag.Value.Type() {
			case "bool":
				rule = "use a boolean value"
			case "duration":
				rule = "use a duration such as 2m or 0"
			case "int":
				rule = "use an integer byte count"
				if flag.Name == "transfers" {
					rule = "use an integer from 1 through 8"
				}
			case "local:device":
				rule = "use local:device ports"
			case "string", "stringArray":
				return
			default:
				t.Fatalf("uncovered registered type %s for %s", flag.Value.Type(), flag.Name)
			}
			t.Run(cmd.Name()+"/"+flag.Name+"/invalid", func(t *testing.T) {
				args := append(append([]string{}, prefix...), "--"+flag.Name+"=PRIVATE_VALUE")
				var out, logs bytes.Buffer
				err := run(t.Context(), args, &out, &logs)
				if pullCode(err) != 2 || out.Len() != 0 {
					t.Fatalf("exit=%d stdout=%s", pullCode(err), &out)
				}
				name := cmd.Name()
				if cmd == root {
					name = "proxy"
				}
				assertConfigRecord(t, logs.Bytes(), name, "--"+flag.Name, rule)
				assertNoSensitiveErrorDetails(t, err, "PRIVATE_VALUE")
			})
		})
	}
}

func TestCLISharedSerialPreflight(t *testing.T) {
	for _, command := range []string{"proxy", "pull"} {
		for _, version := range []bool{false, true} {
			t.Run(command+map[bool]string{true: "/version", false: "/run"}[version], func(t *testing.T) {
				base := t.TempDir()
				dest, trace := filepath.Join(base, "destination"), filepath.Join(base, "trace")
				args := []string{command, "--serial=\n", "--trace=" + trace, "--keylog=" + filepath.Join(base, "keys")}
				if version {
					args = append(args, "--version")
				} else if command == "pull" {
					args = append(args, "--output=json", dest)
				}
				var out, logs bytes.Buffer
				err := runWithSession(t.Context(), args, &out, &logs, func(string, *slog.Logger) (*keylog.Writer, error) { t.Fatal("preflight opened keys"); return nil, nil }, func(context.Context, proxy.Config) (commandSession, error) {
					t.Fatal("preflight opened session")
					return nil, nil
				})
				if pullCode(err) != 2 || out.Len() != 0 {
					t.Fatalf("exit=%d stdout=%s", pullCode(err), &out)
				}
				assertConfigRecord(t, logs.Bytes(), command, "--serial", "use a nonblank device serial of at most 4096 bytes without NUL, CR or LF")
				entries, e := os.ReadDir(base)
				if e != nil || len(entries) != 0 {
					t.Fatalf("preflight created resources: %v %v", entries, e)
				}
			})
		}
	}
}

func TestCLIPullStartupWarnings(t *testing.T) {
	for _, capture := range []string{"open-failure", "worker-failure"} {
		for _, accepted := range []bool{false, true} {
			t.Run(capture+map[bool]string{false: "/runtime-refusal", true: "/accepted"}[accepted], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var out, logs bytes.Buffer
					open := func(string, *slog.Logger) (*keylog.Writer, error) { return nil, errors.New("PRIVATE_KEYLOG_PATH") }
					if capture == "worker-failure" {
						open = func(_ string, logger *slog.Logger) (*keylog.Writer, error) {
							keys := keylog.New(failedStartupKeys{}, logger)
							keys.Write([]byte("PRIVATE_PAYLOAD\n"))
							synctest.Wait()
							return keys, nil
						}
					}
					s := &pullSessionFixture{}
					err := runPullWithSource(t.Context(), []string{"pull", "--output=json", "--keylog=PRIVATE_KEYLOG_PATH", t.TempDir()}, &out, &logs, open,
						func(_ context.Context, cfg proxy.Config) (commandSession, error) {
							cfg.Logger.Info("dependency_startup", "count", 1)
							if !accepted {
								return nil, session.ErrBusy
							}
							return s, nil
						}, func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
							return ctx, nil, session.ErrBusy
						})
					if pullCode(err) != 3 || s.closed != map[bool]int{true: 1, false: 0}[accepted] {
						t.Fatalf("exit=%d closed=%d logs=%s", pullCode(err), s.closed, &logs)
					}
					assertJSONSummary(t, out.String(), 3, "busy", "busy", "none")
					var records []map[string]any
					dec := jsontext.NewDecoder(&logs)
					for {
						var record map[string]any
						err := json.UnmarshalDecode(dec, &record, json.MatchCaseInsensitiveNames(true))
						if err == io.EOF {
							break
						}
						if err != nil {
							t.Fatal(err)
						}
						records = append(records, record)
					}
					warnings, dependencies := 0, 0
					for _, record := range records {
						if record["msg"] == "dependency_startup" {
							dependencies++
						}
						if record["msg"] == "keylog_failed" {
							warnings++
							rule := "use a new writable private key file"
							if capture == "worker-failure" {
								rule = "use a writable private key file"
							}
							if record["option"] != "--keylog" || record["rule"] != rule {
								t.Fatalf("capture attribution=%v", record)
							}
						}
					}
					if warnings != 1 || dependencies != 1 {
						t.Fatalf("warnings=%d dependencies=%d records=%v", warnings, dependencies, records)
					}
				})
			})
		}
	}
}
