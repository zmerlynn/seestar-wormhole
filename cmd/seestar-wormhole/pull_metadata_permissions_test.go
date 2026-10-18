//go:build !windows

package main

import (
	"bytes"
	"context"
	"github.com/go-json-experiment/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
)

// Exercise the real destination checks before either session or SMB startup.
// Literal labels ensure reverting the classification fails at the CLI boundary.
func TestPullMetadataPermissions(t *testing.T) {
	for _, mode := range []string{"direct", "via"} {
		for _, output := range []string{"text", "json"} {
			for _, logFormat := range []string{"text", "json"} {
				for _, target := range []string{"directory", "lock", "state.json"} {
					for _, extra := range []os.FileMode{0040, 0004, 0044} {
						t.Run(mode+"/"+output+"/"+logFormat+"/"+target+"/"+extra.String(), func(t *testing.T) {
							dest, metadata := metadataDiagnosticFixture(t)
							path, perm := metadata, os.FileMode(0700)|extra
							if extra == 0044 {
								perm = 0755 // The incident: chmod -R go+rX.
							}
							if target != "directory" {
								path, perm = filepath.Join(metadata, target), 0600|extra
							}
							if err := os.Chmod(path, perm); err != nil {
								t.Fatal(err)
							}
							out, logs := runMetadataDiagnostic(t, dest, mode, output, logFormat, "metadata_permissions")
							for _, hint := range []string{"0700", "0600", "share delivered files without widening the metadata directory"} {
								if !strings.Contains(logs, hint) {
									t.Errorf("missing recovery hint %q: %s", hint, logs)
								}
							}
							info, err := os.Stat(path)
							if err != nil || info.Mode().Perm() != perm {
								t.Fatalf("permission refusal repaired metadata: info=%v err=%v", info, err)
							}
							if strings.Contains(out+logs, dest) || strings.Contains(out+logs, "PRIVATE_") {
								t.Fatalf("diagnostic leaked a private value: %s %s", out, logs)
							}
						})
					}
				}
				for _, target := range []string{"directory", "lock", "state.json", "non-directory", "non-regular"} {
					t.Run(mode+"/"+output+"/"+logFormat+"/unsafe/"+target, func(t *testing.T) {
						dest, metadata := metadataDiagnosticFixture(t)
						path := metadata
						if target == "lock" || target == "state.json" || target == "non-regular" {
							name := target
							if target == "non-regular" {
								name = "state.json"
							}
							path = filepath.Join(metadata, name)
						}
						if err := os.RemoveAll(path); err != nil {
							t.Fatal(err)
						}
						var err error
						switch target {
						case "non-directory":
							err = os.WriteFile(path, nil, 0644)
						case "non-regular":
							err = os.Mkdir(path, 0755)
						default:
							err = os.Symlink(t.TempDir(), path)
						}
						if err != nil {
							t.Fatal(err)
						}
						runMetadataDiagnostic(t, dest, mode, output, logFormat, "unsafe_path")
					})
				}
			}
		}
	}
}

func metadataDiagnosticFixture(t *testing.T) (string, string) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "PRIVATE_DEST")
	d, err := pull.OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	d.State.Source = "direct:PRIVATE_SOURCE"
	data, err := json.Marshal(d.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(dest, ".seestar-wormhole")
	if err := os.WriteFile(filepath.Join(metadata, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return dest, metadata
}

func runMetadataDiagnostic(t *testing.T, dest, mode, output, logFormat, reason string) (string, string) {
	t.Helper()
	args := []string{"pull", "--output=" + output, "--log-format=" + logFormat}
	if mode == "via" {
		args = append(args, "--via=127.0.0.1:1")
	}
	args = append(args, dest)
	var out, logs bytes.Buffer
	err := runPullWithSource(t.Context(), args, &out, &logs, keylog.Open,
		func(context.Context, proxy.Config) (commandSession, error) {
			t.Fatal("metadata refusal reached device session startup")
			return nil, nil
		},
		func(context.Context, func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
			t.Fatal("metadata refusal reached SMB startup/listing")
			return nil, nil, nil
		})
	if pullCode(err) != 1 {
		t.Fatalf("exit=%d want 1: %s %s", pullCode(err), &out, &logs)
	}
	if output == "json" {
		assertJSONSummary(t, out.String(), 1, "error", reason, "none")
	} else if !strings.Contains(out.String(), "status=error ") || !strings.Contains(out.String(), " reason="+reason+" cleanup_reason=none\n") {
		t.Fatalf("unexpected text summary: %s", &out)
	}
	if logFormat == "json" {
		var record struct {
			Reason string
			Leaves []string `json:"leaf_causes"`
		}
		if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		if record.Reason != reason || !slices.Equal(record.Leaves, []string{reason}) {
			t.Fatalf("diagnostic=%+v want reason/sole leaf %s", record, reason)
		}
	} else if !strings.Contains(logs.String(), "reason="+reason+" ") || !strings.Contains(logs.String(), "leaf_causes=["+reason+"]") {
		t.Fatalf("unexpected text diagnostic: %s", &logs)
	}
	return out.String(), logs.String()
}

func TestPullHelpMetadataPermissions(t *testing.T) {
	var out bytes.Buffer
	_, _ = parse([]string{"pull", "--help"}, &out)
	for _, hint := range []string{"--dry-run", "docs/reference.md"} {
		if !strings.Contains(out.String(), hint) {
			t.Errorf("pull --help missing %q", hint)
		}
	}
}
