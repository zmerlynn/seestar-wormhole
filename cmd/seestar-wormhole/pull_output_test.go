package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-json-experiment/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// The literal is an independent contract fixture: renames, extra fields, missing
// zero counts, numeric strings, or nested bounds fail this comparison.
func assertJSONSummary(t *testing.T, output string, code int, status, reason, cleanup string) {
	t.Helper()
	var got map[string]any
	dec := jsontext.NewDecoder(strings.NewReader(output))
	if err := json.UnmarshalDecode(dec, &got, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatalf("summary: %v: %s", err, output)
	}
	if err := json.UnmarshalDecode(dec, new(any), json.MatchCaseInsensitiveNames(true)); err != io.EOF {
		t.Fatalf("extra stdout: %v: %s", err, output)
	}
	if got["exit_code"] != float64(code) || got["status"] != status || got["reason"] != reason || got["cleanup_reason"] != cleanup {
		t.Fatalf("summary=%v want exit=%d status=%s reason=%s cleanup=%s", got, code, status, reason, cleanup)
	}
	for _, name := range []string{"lower", "upper"} {
		if _, ok := got[name].(string); !ok {
			t.Fatalf("%s not string: %v", name, got)
		}
		got[name] = "BOUND"
	}
	got["status"], got["reason"], got["cleanup_reason"], got["exit_code"] = "STATUS", "REASON", "CLEANUP", float64(99)
	got["first_scan"] = false // covered separately by the engine tests
	canonical, err := json.Marshal(got, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	const golden = `{"bytes":0,"cleanup_reason":"CLEANUP","copied":0,"deferred":0,"exit_code":99,"first_scan":false,"lower":"BOUND","planned":0,"reason":"REASON","status":"STATUS","upper":"BOUND","verified":0}`
	if string(canonical) != golden {
		t.Fatalf("JSON shape got %s want %s", canonical, golden)
	}
}

type outputFailureFs struct {
	fs.Fs
	listErr, shutdownErr error
	beforeShutdown       func()
}

func (f outputFailureFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.Fs.List(ctx, dir)
}
func (f outputFailureFs) Features() *fs.Features {
	features := *f.Fs.Features()
	features.Shutdown = func(context.Context) error {
		if f.beforeShutdown != nil {
			f.beforeShutdown()
		}
		return f.shutdownErr
	}
	return &features
}

func TestPullJSONExitPaths(t *testing.T)       { testPullJSONExitPaths(t, false) }
func TestPullDryRunJSONExitPaths(t *testing.T) { testPullJSONExitPaths(t, true) }

func testPullJSONExitPaths(t *testing.T, dry bool) {
	for _, tc := range []struct {
		name, phase             string
		cause                   error
		code                    int
		status, reason, cleanup string
	}{
		{"success", "", nil, 0, "nothing-new", "none", "none"},
		{"empty", "empty", nil, 4, "nothing-new", "none", "none"},
		{"destination", "destination", errors.ErrUnsupported, 1, "error", "not_a_directory", "none"},
		{"session", "session", errors.ErrUnsupported, 1, "error", "operation_failed", "none"},
		{"credentials", "session", cloud.ErrConfig, 2, "error", "invalid_configuration", "none"},
		{"busy", "session", session.ErrBusy, 3, "busy", "busy", "none"},
		{"locked", "locked", nil, 3, "busy", "lock_held", "none"},
		{"source-mismatch", "binding", nil, 2, "error", "source_mismatch", "none"},
		{"trace", "trace", nil, 2, "error", "invalid_option", "none"},
		{"source", "source", errors.ErrUnsupported, 1, "error", "operation_failed", "none"},
		{"timeout", "timeout", nil, 1, "error", "timeout", "none"},
		{"interrupt", "source", context.Canceled, 130, "canceled", "canceled", "none"},
		{"inventory", "list", errors.ErrUnsupported, 1, "error", "operation_failed", "none"},
		{"shutdown", "shutdown", errors.ErrUnsupported, 1, "error", "operation_failed", "none"},
		{"release", "release", session.ErrStop, 1, "error", "release_failed", "release_failed"},
		{"release-overrides-interrupt", "release-cancel", session.ErrStop, 1, "error", "release_failed", "release_failed"},
		{"commit", "commit", nil, 1, "error", "operation_failed", "none"},
	} {
		// Dry-run has no writer lock or commit and refuses capture flags.
		if dry && (tc.phase == "locked" || tc.phase == "commit" || tc.phase == "trace") {
			continue
		}
		if dry {
			if tc.phase == "" || tc.phase == "empty" {
				tc.code, tc.status = 0, "planned"
			}
		}
		t.Run(tc.name+map[bool]string{false: "", true: "/dry-run"}[dry], func(t *testing.T) {
			dest, root := t.TempDir(), t.TempDir()
			args := []string{"pull", "--output=json", "--settle=0"}
			if dry {
				args = append(args, "--dry-run")
			}
			if tc.phase == "empty" {
				args = append(args, "--error-on-empty")
			}
			if tc.phase == "timeout" {
				args = append(args, "--timeout=1ns")
			}
			if tc.phase == "destination" {
				dest = filepath.Join(dest, "file")
				if err := os.WriteFile(dest, []byte("file"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.phase == "binding" || tc.phase == "locked" {
				d, err := pull.OpenDestination(dest)
				if err != nil {
					t.Fatal(err)
				}
				if tc.phase == "binding" {
					if err = d.RebindSource("other-source"); err != nil {
						t.Fatal(err)
					}
					d.Close()
				} else {
					defer d.Close()
				}
			}
			if tc.phase == "trace" {
				args = append(args, "--trace="+dest)
			}
			before := snapshotTree(t, dest)
			args = append(args, dest)
			s := &pullSessionFixture{}
			var out, logs bytes.Buffer
			err := runPullWithSource(t.Context(), args, &out, &logs, keylog.Open,
				func(context.Context, proxy.Config) (commandSession, error) {
					if tc.phase == "session" {
						return nil, tc.cause
					}
					if strings.HasPrefix(tc.phase, "release") {
						s.closeErr = tc.cause
					}
					return s, nil
				}, func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
					switch tc.phase {
					case "source":
						return ctx, nil, tc.cause
					case "timeout":
						<-ctx.Done()
						return ctx, nil, ctx.Err()
					case "release-cancel":
						return ctx, nil, context.Canceled
					}
					source, err := local.NewFs(ctx, "fixture", root, configmap.Simple{})
					if err != nil {
						t.Fatal(err)
					}
					wrapped := outputFailureFs{Fs: source}
					if tc.phase == "list" {
						wrapped.listErr = tc.cause
					}
					if tc.phase == "shutdown" {
						wrapped.shutdownErr = tc.cause
					}
					if tc.phase == "commit" {
						wrapped.beforeShutdown = func() {
							if err := os.Mkdir(filepath.Join(dest, "SHA256SUMS"), 0700); err != nil {
								t.Fatal(err)
							}
						}
					}
					return ctx, wrapped, nil
				})
			if got := pullCode(err); got != tc.code {
				t.Fatalf("code=%d want=%d logs=%s", got, tc.code, &logs)
			}
			assertJSONSummary(t, out.String(), tc.code, tc.status, tc.reason, tc.cleanup)
			if dry && !reflect.DeepEqual(before, snapshotTree(t, dest)) {
				t.Fatal("failed dry-run changed destination")
			}
			wantRelease := 1
			if tc.phase == "session" || tc.phase == "locked" || tc.phase == "destination" || tc.phase == "trace" {
				wantRelease = 0
			}
			if s.closed != wantRelease {
				t.Fatalf("releases=%d want=%d", s.closed, wantRelease)
			}
		})
	}
}

func TestPullJSONPreflightHasNoSummary(t *testing.T) {
	for _, flags := range [][]string{{"--output=json"}, {"--output=json", "--transfers=9", "dest"}, {"--output=yaml", "dest"}, {"--output=json", "--unknown", "dest"}, {"--output=json", "--help"}, {"--output=json", "--version"}} {
		var out, logs bytes.Buffer
		err := runPull(t.Context(), append([]string{"pull"}, flags...), &out, &logs, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) {
			t.Fatal("preflight opened session")
			return nil, nil
		})
		informational := flags[len(flags)-1] == "--help" || flags[len(flags)-1] == "--version"
		if !informational && (pullCode(err) != 2 || out.Len() != 0) {
			t.Fatalf("flags=%v exit=%d out=%s", flags, pullCode(err), &out)
		}
		if informational && (pullCode(err) != 0 || strings.HasPrefix(out.String(), "{")) {
			t.Fatalf("informational exit=%d out=%s", pullCode(err), &out)
		}
	}
}

type failingSummaryWriter struct{}

func (failingSummaryWriter) Write([]byte) (int, error) { return 0, errors.ErrUnsupported }
func TestPullJSONWriteFailure(t *testing.T) {
	err := runPull(t.Context(), []string{"pull", "--output=json", t.TempDir()}, failingSummaryWriter{}, io.Discard, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) { return nil, session.ErrBusy })
	if pullCode(err) != 1 || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("write failure=%v code=%d", err, pullCode(err))
	}
}

func TestPullSummaryGolden(t *testing.T) {
	s := pull.Result{Status: "error", Copied: 17, Verified: 19, Deferred: 3,
		Bytes: 123456789, FirstScan: true, Reason: "timeout", CleanupReason: "none",
		Bounds: pull.Bounds{Lower: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC),
			Upper: time.Date(2026, 10, 14, 6, 0, 0, 123, time.UTC)}}
	for _, tc := range []struct{ format, golden string }{
		{"json", `{"planned":0,"status":"error","copied":17,"verified":19,"deferred":3,"bytes":123456789,"first_scan":true,"lower":"2026-10-14T00:00:00Z","upper":"2026-10-14T06:00:00.000000123Z","reason":"timeout","cleanup_reason":"none","exit_code":1}` + "\n"},
		{"text", "status=error copied=17 verified=19 deferred=3 bytes=123456789 first_scan=true lower=2026-10-14T00:00:00Z upper=2026-10-14T06:00:00.000000123Z reason=timeout cleanup_reason=none\n"},
	} {
		var out bytes.Buffer
		if err := writePullSummary(&out, tc.format, s, 1); err != nil {
			t.Fatal(err)
		}
		if out.String() != tc.golden {
			t.Fatalf("%s got %s want %s", tc.format, &out, tc.golden)
		}
	}
}
