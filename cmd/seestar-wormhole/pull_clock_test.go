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
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
)

// Port of the M2 command/runtime probe from review/e2e-pull/probes/command_probe_test.go.
func TestE2EPullNewFileDuringStartup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root, dest := t.TempDir(), t.TempDir()
		started := time.Now().UTC()
		clockPutFile(t, root, "old.fit", started.Add(-time.Hour))
		s := &pullSessionFixture{}
		var out, logs bytes.Buffer
		err := runPullWithSource(t.Context(), []string{"pull", "--output=json", dest}, &out, &logs, keylog.Open,
			func(context.Context, proxy.Config) (commandSession, error) { return s, nil },
			func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
				time.Sleep(2 * time.Second) // synctest advances virtual time during setup.
				clockPutFile(t, root, "new.fit", time.Now().Add(-time.Second))
				src, err := local.NewFs(ctx, "fixture", root, configmap.Simple{})
				return ctx, src, err
			})
		if err != nil || s.closed != 1 {
			t.Fatalf("ordinary post-start file: err=%v closed=%d stdout=%s logs=%s", err, s.closed, &out, &logs)
		}
		if data, err := os.ReadFile(filepath.Join(dest, "old.fit")); err != nil || string(data) != "old.fit" {
			t.Fatalf("settled older file: bytes=%q err=%v", data, err)
		}
		summary := clockSummary(t, out.Bytes(), 0, "copied", "none", "none")
		if summary["copied"] != float64(1) || summary["deferred"] != float64(1) || summary["upper"] != started.Add(-2*time.Minute).Format(time.RFC3339Nano) {
			t.Fatalf("summary=%v", summary)
		}
	})
}

func clockPutFile(t *testing.T, root, name string, stamp time.Time) {
	t.Helper()
	filename := filepath.Join(root, name)
	if err := os.WriteFile(filename, []byte(name), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filename, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

// List and ModTime can both spend time obtaining source metadata. Neither may
// refresh selection bounds, nor use a clock captured before that work for skew.
type clockInventoryFs struct {
	fs.Fs
	t        *testing.T
	root     string
	metadata bool
	skew     time.Duration
}

func (f clockInventoryFs) Features() *fs.Features {
	features := *f.Fs.Features()
	features.ListR = nil
	return &features
}

func (f clockInventoryFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	if !f.metadata {
		time.Sleep(3 * time.Minute)
	}
	clockPutFile(f.t, f.root, "new.fit", time.Now())
	entries, err := f.Fs.List(ctx, dir)
	for i, entry := range entries {
		if o, ok := entry.(fs.Object); ok && o.Remote() == "new.fit" && f.metadata {
			entries[i] = clockMetadataObject{Object: o, skew: f.skew}
		}
	}
	return entries, err
}

type clockMetadataObject struct {
	fs.Object
	skew time.Duration
}

func (o clockMetadataObject) ModTime(context.Context) time.Time {
	time.Sleep(3 * time.Minute)
	return time.Now().Add(o.skew)
}

func TestPullObservationClock(t *testing.T) {
	for _, tc := range []struct {
		name     string
		flags    []string
		metadata bool
		skew     time.Duration
	}{
		{name: "slow-inventory"},
		{name: "metadata-observation", metadata: true},
		{name: "genuine-skew", metadata: true, skew: time.Nanosecond},
		{name: "genuine-skew-until", flags: []string{"--until=30m"}, metadata: true, skew: time.Nanosecond},
		{name: "duration-since", flags: []string{"--since=1h"}},
		{name: "duration-until", flags: []string{"--until=30m"}},
		{name: "absolute-until", flags: []string{"--until=absolute"}},
		{name: "zero-settle", flags: []string{"--settle=0"}},
	} {
		for _, dry := range []bool{false, true} {
			name := tc.name
			if dry {
				name += "/dry-run"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					started := time.Now().UTC()
					root, parent := t.TempDir(), t.TempDir()
					dest := filepath.Join(parent, "dest")
					before := snapshotTree(t, parent)
					clockPutFile(t, root, "old.fit", started.Add(-time.Hour))
					args := []string{"pull", "--output=json"}
					upper := started.Add(-2 * time.Minute)
					lower := time.Time{}
					auto := true
					for _, flag := range tc.flags {
						switch flag {
						case "--since=1h":
							lower = started.Add(-time.Hour)
							auto = false
						case "--until=30m":
							upper = started.Add(-30 * time.Minute)
						case "--until=absolute":
							upper = started.Add(-30 * time.Minute)
							flag = "--until=" + upper.Format(time.RFC3339Nano)
						case "--settle=0":
							upper = started
						}
						args = append(args, flag)
					}
					if dry {
						args = append(args, "--dry-run")
					}
					args = append(args, dest)
					s := &pullSessionFixture{}
					var out, logs bytes.Buffer
					err := runPullWithSource(t.Context(), args, &out, &logs, keylog.Open,
						func(context.Context, proxy.Config) (commandSession, error) { return s, nil },
						func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
							src, err := local.NewFs(ctx, "fixture", root, configmap.Simple{})
							return ctx, clockInventoryFs{Fs: src, t: t, root: root, metadata: tc.metadata, skew: tc.skew}, err
						})
					code, status, reason := 0, "copied", "none"
					copied, verified, planned, deferred, total := float64(1), float64(1), float64(0), float64(1), float64(len("old.fit"))
					if dry {
						status = "planned"
						copied = 0
						verified = 0
						planned = 1
					}
					if tc.skew > 0 {
						code, status, reason = 1, "error", "future_mtime"
						copied, verified, planned, deferred, total = 0, 0, 0, 0, 0
					}
					if pullCode(err) != code || s.closed != 1 {
						t.Fatalf("exit=%d want=%d closed=%d out=%s logs=%s", pullCode(err), code, s.closed, &out, &logs)
					}
					summary := clockSummary(t, out.Bytes(), code, status, reason, "none")
					for key, want := range map[string]any{
						"copied": copied, "verified": verified, "planned": planned, "deferred": deferred, "bytes": total,
						"first_scan": true, "lower": lower.Format(time.RFC3339Nano), "upper": upper.Format(time.RFC3339Nano),
					} {
						if summary[key] != want {
							t.Errorf("%s=%v want=%v; summary=%v", key, summary[key], want, summary)
						}
					}
					if dry {
						if after := snapshotTree(t, parent); !reflect.DeepEqual(before, after) {
							t.Fatalf("dry-run mutated destination: before=%v after=%v", before, after)
						}
						return
					}
					if _, err := os.Stat(filepath.Join(dest, "new.fit")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("post-start file copied: %v", err)
					}
					data, readErr := os.ReadFile(filepath.Join(dest, "old.fit"))
					if code == 0 && (readErr != nil || string(data) != "old.fit") {
						t.Fatalf("settled file: bytes=%q err=%v", data, readErr)
					}
					if code != 0 && !errors.Is(readErr, os.ErrNotExist) {
						t.Fatalf("future skew allowed older copy: %v", readErr)
					}
					d, err := pull.OpenReadOnlyDestination(dest)
					if err != nil {
						t.Fatal(err)
					}
					defer d.Close()
					if auto && code == 0 {
						if len(d.State.Selections) != 1 {
							t.Fatalf("selections=%v", d.State.Selections)
						}
						for _, selection := range d.State.Selections {
							if !selection.Checkpoint.Equal(upper) {
								t.Fatalf("checkpoint=%s want=%s", selection.Checkpoint, upper)
							}
						}
					} else if len(d.State.Selections) != 0 {
						t.Fatalf("unexpected checkpoint: %v", d.State.Selections)
					}
				})
			})
		}
	}
}

func clockSummary(t *testing.T, output []byte, code int, status, reason, cleanup string) map[string]any {
	t.Helper()
	var got map[string]any
	dec := jsontext.NewDecoder(bytes.NewReader(output))
	if err := json.UnmarshalDecode(dec, &got, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatalf("summary: %v: %s", err, output)
	}
	if err := json.UnmarshalDecode(dec, new(any), json.MatchCaseInsensitiveNames(true)); err != io.EOF {
		t.Fatalf("extra stdout: %v: %s", err, output)
	}
	for key, want := range map[string]any{"exit_code": float64(code), "status": status, "reason": reason, "cleanup_reason": cleanup} {
		if got[key] != want {
			t.Errorf("%s=%v want=%v; summary=%v", key, got[key], want, got)
		}
	}
	return got
}
