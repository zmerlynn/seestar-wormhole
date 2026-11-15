package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/cli"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// Compare the complete record, allowing only slog's timestamp to vary.
// assertConfigRecord checks that the last line of logs is the configuration
// refusal, and returns the value it names, if any. Lines before it are the
// startup's own records, which a refusal does not suppress.
func assertConfigRecord(t *testing.T, logs []byte, command, option, rule string) string {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(logs), []byte("\n"))
	var got map[string]any
	if fileCommand(command) { // a file command's record is text
		var err error
		if got, err = textRecord(string(lines[len(lines)-1])); err != nil || len(lines) != 1 {
			t.Fatalf("want exactly one text diagnostic: %s: %v", logs, err)
		}
	} else if err := json.Unmarshal(lines[len(lines)-1], &got, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatalf("want exactly one JSON diagnostic: %s: %v", logs, err)
	}
	if _, ok := got["time"].(string); !ok {
		t.Fatalf("missing timestamp: %v", got)
	}
	delete(got, "time")
	value, _ := got["value"].(string)
	delete(got, "value")
	delete(got, "error") // a dependency's own text, checked where it is set
	want := map[string]any{"level": "ERROR", "msg": command + " failed", "option": option, "rule": rule, "help": programName + " " + command + " --help", "phase": "arguments", "reason": "invalid_option"}
	if command == "proxy" || command == "discovery" {
		want["cleanup_reason"] = "none"
	}
	if command == programName { // a root usage error is the program's own
		want["help"] = programName + " --help"
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic=%v want=%v", got, want)
	}
	return value
}

func TestCLIConfigAttribution(t *testing.T) {
	for _, command := range []string{"proxy"} {
		for _, format := range []string{"text", "json"} {
			for _, problem := range []string{"email", "password", "state", "selection"} {
				t.Run(command+"/"+format+"/"+problem, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						t.Setenv("SEESTAR_EMAIL", "test@example.invalid")
						t.Setenv("SEESTAR_PASSWORD", "PRIVATE_PASSWORD")
						args := []string{command, "--log-format=" + format, "--log-level=debug"}
						option, rule := "SEESTAR_EMAIL", "use a valid UTF-8 email of at most 4096 bytes matching the supported account format"
						state := ""
						switch problem {
						case "email":
							t.Setenv("SEESTAR_EMAIL", "")
						case "password":
							t.Setenv("SEESTAR_PASSWORD", "bad")
							option, rule = "SEESTAR_PASSWORD", "use a valid UTF-8 password of 8..32 UTF-16 units"
						case "state":
							state = filepath.Join(t.TempDir(), "state")
							if err := os.WriteFile(state, []byte("block directory"), 0600); err != nil {
								t.Fatal(err)
							}
							args = append(args, "--state-dir="+state)
							option, rule = "--state-dir", "use a private writable state directory and installation identity file"
						case "selection":
							option, rule = "--serial", "select an existing device from the account catalog"
						}
						var out, logs bytes.Buffer
						factory := func(ctx context.Context, cfg proxy.Config) (commandSession, error) {
							if problem == "selection" {
								// A preliminary dependency record survives a typed refusal.
								cfg.Logger.Debug("dependency_startup", "count", 1)
								return nil, diagnostic.Wrap("device_selection", "invalid_configuration", errors.Join(cloud.ErrConfig, &argumentError{Option: option, Rule: rule}))
							}
							return proxy.New(ctx, cfg)
						}
						// Ephemeral loopback listeners avoid collisions and never reach a device:
						// each real construction fails local credential/state validation.
						err := runWithListener(t.Context(), args, &out, &logs, factory, func(_, _ string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") })
						if got := commandExitCode(options{command: commandProxy}, err); got != 2 {
							t.Fatalf("exit=%d want=2 err=%v logs=%s", got, err, &logs)
						}
						if value := assertConfigRecord(t, logs.Bytes(), command, option, rule); value != state {
							t.Errorf("value=%q want %q", value, state)
						}
						if problem == "state" && !strings.Contains(logs.String(), `"error":"`) && !strings.Contains(logs.String(), "error=") {
							t.Errorf("state failure lost the OS error: %s", &logs)
						}
						if problem == "selection" && !strings.Contains(logs.String(), "dependency_startup") {
							t.Errorf("dependency record suppressed: %s", &logs)
						}
						assertNoSecrets(t, err, "PRIVATE_PASSWORD", "PRIVATE_PAYLOAD")
						if out.Len() != 0 {
							t.Fatalf("proxy stdout=%s", &out)
						}
					})
				})
			}
		}
	}
}

func TestCLIParserRules(t *testing.T) {
	var o options
	root := newCommand(&o, io.Discard)
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
			rule, fileRule := cli.ValueRule(flag.Value.Type())
			switch typ := flag.Value.Type(); {
			case typ == "string" || typ == "stringArray":
				return
			case fileCommand(cmd.Name()):
				// A file command's values have the copy rules.
				if !fileRule {
					t.Fatalf("uncovered registered type %s for %s %s", typ, cmd.Name(), flag.Name)
				}
			case typ == "bool":
				rule = "use a boolean value"
			case typ == "duration":
				rule = "use a duration such as 2m or 0"
			case typ == "int":
				rule = "use an integer byte count"
			case typ == "local:device":
				rule = "use local:device ports"
			default:
				t.Fatalf("uncovered registered type %s for %s", typ, flag.Name)
			}
			t.Run(cmd.Name()+"/"+flag.Name+"/invalid", func(t *testing.T) {
				args := append(append([]string{}, prefix...), "--"+flag.Name+"=PRIVATE_VALUE")
				var out, logs bytes.Buffer
				err := run(t.Context(), args, &out, &logs)
				if commandExitCode(options{command: commandProxy}, err) != 2 || out.Len() != 0 {
					t.Fatalf("exit=%d stdout=%s", commandExitCode(options{command: commandProxy}, err), &out)
				}
				name := cmd.Name()
				if value := assertConfigRecord(t, logs.Bytes(), name, "--"+flag.Name, rule); value != "PRIVATE_VALUE" || !strings.Contains(err.Error(), "PRIVATE_VALUE") {
					t.Fatalf("value not named: value=%q err=%v", value, err)
				}
			})
		})
	}
}

func TestCLISharedSerialPreflight(t *testing.T) {
	for _, command := range []string{"proxy"} {
		for _, version := range []bool{false, true} {
			t.Run(command+map[bool]string{true: "/version", false: "/run"}[version], func(t *testing.T) {
				args := []string{command, "--serial=\n"}
				if version {
					args = append(args, "--version")
				}
				var out, logs bytes.Buffer
				err := runWithSession(t.Context(), args, &out, &logs, func(context.Context, proxy.Config) (commandSession, error) {
					t.Fatal("preflight opened session")
					return nil, nil
				})
				if commandExitCode(options{command: commandProxy}, err) != 2 || out.Len() != 0 {
					t.Fatalf("exit=%d stdout=%s", commandExitCode(options{command: commandProxy}, err), &out)
				}
				assertConfigRecord(t, logs.Bytes(), command, "--serial", "use a device serial of 1-64 printable ASCII bytes (0x21-0x7E; no spaces)")
			})
		}
	}
}
