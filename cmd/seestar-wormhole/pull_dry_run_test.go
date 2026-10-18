package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/local"
	rfs "github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type treeEntry struct {
	Mode  fs.FileMode
	Mtime time.Time
	Bytes string
}

func snapshotTree(t *testing.T, root string) map[string]treeEntry {
	t.Helper()
	result := map[string]treeEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		record := treeEntry{Mode: info.Mode(), Mtime: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			record.Bytes = string(data)
		}
		result[rel] = record
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type noReadPlanFs struct {
	rfs.Fs
	opens *int
}

func (f noReadPlanFs) List(ctx context.Context, dir string) (rfs.DirEntries, error) {
	entries, err := f.Fs.List(ctx, dir)
	for i, entry := range entries {
		if o, ok := entry.(rfs.Object); ok {
			entries[i] = noReadPlanObject{o, f.opens}
		}
	}
	return entries, err
}
func (f noReadPlanFs) NewObject(ctx context.Context, name string) (rfs.Object, error) {
	o, err := f.Fs.NewObject(ctx, name)
	if err != nil {
		return nil, err
	}
	return noReadPlanObject{o, f.opens}, nil
}

type noReadPlanObject struct {
	rfs.Object
	opens *int
}

func (o noReadPlanObject) Open(context.Context, ...rfs.OpenOption) (io.ReadCloser, error) {
	*o.opens++
	return nil, errors.New("dry-run opened source content")
}

func TestPullDryRunDestinationUnchanged(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		for _, level := range []string{"info", "debug"} {
			t.Run(strings.Join([]string{map[bool]string{false: "missing", true: "seeded"}[seeded], level}, "/"), func(t *testing.T) {
				parent, root := t.TempDir(), t.TempDir()
				dest := filepath.Join(parent, "dest")
				stamp := time.Now().Add(-time.Hour)
				put := func(name, data string, mtime time.Time) {
					filename := filepath.Join(root, name)
					if err := os.WriteFile(filename, []byte(data), 0644); err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(filename, mtime, mtime); err != nil {
						t.Fatal(err)
					}
				}
				put("skip.fit", "receipt", stamp)
				source, err := local.NewFs(t.Context(), "fixture", root, configmap.Simple{})
				if err != nil {
					t.Fatal(err)
				}
				run := func(flags []string, src rfs.Fs, closeErr error) (int, map[string]any, string) {
					t.Helper()
					s := &pullSessionFixture{closeErr: closeErr}
					sessions := 0
					var out, logs bytes.Buffer
					args := append([]string{"pull", "--output=json"}, flags...)
					args = append(args, dest)
					err := runPullWithSource(t.Context(), args, &out, &logs, keylog.Open,
						func(context.Context, proxy.Config) (commandSession, error) { sessions++; return s, nil },
						func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, rfs.Fs, error) {
							return ctx, src, nil
						})
					if sessions != 1 || s.closed != 1 {
						t.Fatalf("sessions=%d releases=%d", sessions, s.closed)
					}
					var summary map[string]any
					if e := json.Unmarshal(out.Bytes(), &summary, json.MatchCaseInsensitiveNames(true)); e != nil {
						t.Fatalf("JSON: %v out=%s logs=%s", e, &out, &logs)
					}
					if summary["exit_code"] != float64(pullCode(err)) {
						t.Fatalf("exit field=%v exit=%d", summary, pullCode(err))
					}
					return pullCode(err), summary, logs.String()
				}
				if seeded {
					if code, _, _ := run([]string{"--settle=0"}, source, nil); code != 0 {
						t.Fatal(code)
					}
					// Would ordinarily be removed by OpenDestination's recovery sweep.
					if err := os.WriteFile(filepath.Join(dest, "old.seestar-part-0123456789abcdef0123456789abcdef"), []byte("keep partial"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				put("copy.fit", "new", stamp)
				put("defer.fit", "recent", time.Now().Add(-time.Second))
				before := snapshotTree(t, parent)
				opens := 0
				code, summary, logs := run([]string{"--dry-run", "--error-on-empty", "--log-level=" + level}, noReadPlanFs{source, &opens}, nil)
				planned, total := float64(2), float64(10)
				if seeded {
					planned, total = 1, 3
				}
				if code != 0 || summary["status"] != "planned" || summary["planned"] != planned || summary["bytes"] != total || summary["copied"] != float64(0) || summary["verified"] != float64(0) || summary["deferred"] != float64(1) {
					t.Fatalf("plan=%v exit=%d logs=%s", summary, code, logs)
				}
				if opens != 0 {
					t.Fatalf("source content reads=%d", opens)
				}
				if after := snapshotTree(t, parent); !reflect.DeepEqual(before, after) {
					t.Fatalf("destination mutated: before=%v after=%v", before, after)
				}
				if level == "debug" {
					for _, field := range []string{"copy.fit", "defer.fit", `"action":"copy"`, `"action":"defer"`} {
						if !strings.Contains(logs, field) {
							t.Fatalf("plan missing %s: %s", field, logs)
						}
					}
					if seeded && (!strings.Contains(logs, "skip.fit") || !strings.Contains(logs, `"action":"skip"`)) {
						t.Fatal(logs)
					}
				} else if strings.Contains(logs, ".fit") {
					t.Fatalf("routine level lists files: %s", logs)
				}

				for _, plan := range []struct {
					flag                   string
					count, bytes, deferred float64
				}{
					{"--verify", 2, 10, 1}, {"--recheck", 2, 10, 1},
					{"--since=30m", 0, 0, 1}, {"--until=3h", 0, 0, 3},
					{"--only=^no-match$", 0, 0, 0},
				} {
					flags := []string{"--dry-run", "--error-on-empty", "--log-level=debug", plan.flag}
					if plan.flag == "--recheck" {
						flags = append(flags, "--since=all")
					}
					code, summary, logs := run(flags, noReadPlanFs{source, &opens}, nil)
					if code != 0 || summary["status"] != "planned" || summary["planned"] != plan.count || summary["bytes"] != plan.bytes || summary["deferred"] != plan.deferred {
						t.Fatalf("flag=%s exit=%d summary=%v", plan.flag, code, summary)
					}
					if seeded && (plan.flag == "--verify" || plan.flag == "--recheck") && !strings.Contains(logs, `"action":"check"`) {
						t.Fatalf("missing comparison plan: %s", logs)
					}
					if opens != 0 || !reflect.DeepEqual(before, snapshotTree(t, parent)) {
						t.Fatalf("flag=%s read content or mutated destination", plan.flag)
					}
				}
				// Failed release still leaves the exact same tree and projects exit 1.
				code, summary, _ = run([]string{"--dry-run"}, noReadPlanFs{source, &opens}, session.ErrStop)
				if code != 1 || summary["status"] != "error" || summary["cleanup_reason"] != "release_failed" {
					t.Fatalf("release=%v exit=%d", summary, code)
				}
				if after := snapshotTree(t, parent); !reflect.DeepEqual(before, after) {
					t.Fatal("release failure mutated destination")
				}
			})
		}
	}
}

func TestPullDirectoriesDryRunDestinationUnchanged(t *testing.T) {
	for _, bound := range []bool{false, true} {
		t.Run(fmt.Sprintf("bound=%t", bound), func(t *testing.T) {
			root, parent := t.TempDir(), t.TempDir()
			dest := filepath.Join(parent, "archive")
			stamp := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
			put := func(name, data string) {
				t.Helper()
				filename := filepath.Join(root, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filename, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(filename, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			put("MyWorks/skip.fit", "receipt")
			if bound {
				if code, out, _, _ := runDirectoryCommand(t, root, dest, "--dir=MyWorks"); code != 0 {
					t.Fatalf("setup: exit=%d output=%s", code, out)
				}
			}
			put("MyWorks/copy.fit", "light")
			put("Autorun/Dark/b.fit", "dark")
			put("Autorun/Flat/c.fit", "flat")
			source, err := local.NewFs(t.Context(), "fixture", root, configmap.Simple{})
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotTree(t, parent)
			opens := 0
			var out, logs bytes.Buffer
			s := &pullSessionFixture{}
			err = runPullWithSource(t.Context(), []string{"pull", "--dry-run", "--settle=0", "--since=all", "--output=json",
				"--dir=MyWorks", "--dir=Autorun/Dark", "--dir=Autorun/Flat", dest}, &out, &logs, keylog.Open,
				func(context.Context, proxy.Config) (commandSession, error) { return s, nil },
				func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, rfs.Fs, error) {
					return ctx, noReadPlanFs{source, &opens}, nil
				})
			var summary map[string]any
			if e := json.Unmarshal(out.Bytes(), &summary, json.MatchCaseInsensitiveNames(true)); e != nil {
				t.Fatalf("JSON: %v output=%s logs=%s", e, &out, &logs)
			}
			planned, total := float64(4), float64(20)
			if bound {
				planned, total = 3, 13
			}
			if pullCode(err) != 0 || summary["exit_code"] != float64(0) || summary["status"] != "planned" ||
				summary["planned"] != planned || summary["bytes"] != total || summary["copied"] != float64(0) ||
				summary["verified"] != float64(0) || summary["deferred"] != float64(0) {
				t.Errorf("exit=%d summary=%v logs=%s", pullCode(err), summary, &logs)
			}
			if opens != 0 || s.closed != 1 {
				t.Errorf("content opens=%d want 0; releases=%d want 1", opens, s.closed)
			}
			// Includes state bytes, receipts, manifests, locks, directories and mtimes.
			if after := snapshotTree(t, parent); !reflect.DeepEqual(before, after) {
				t.Errorf("destination changed: before=%v after=%v", before, after)
			}
		})
	}
}

func TestPullDryRunRejectsWriteOptions(t *testing.T) {
	for _, flag := range []string{"--state-dir=", "--trace=", "--keylog=", "--rebind=false"} {
		var out, logs bytes.Buffer
		err := runPull(t.Context(), []string{"pull", "--dry-run", "--output=json", flag, t.TempDir()}, &out, &logs, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) {
			t.Fatal("invalid dry-run opened session")
			return nil, nil
		})
		if pullCode(err) != 2 || out.Len() != 0 {
			t.Fatalf("flag=%s exit=%d out=%s", flag, pullCode(err), &out)
		}
	}
}

func TestPullDryRunVia(t *testing.T) {
	parent, root := t.TempDir(), t.TempDir()
	before := snapshotTree(t, parent)
	src, err := local.NewFs(t.Context(), "fixture", root, configmap.Simple{})
	if err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	err = runPullWithSource(t.Context(), []string{"pull", "--dry-run", "--output=json", "--via=[::1]:4445", filepath.Join(parent, "missing")}, &out, &logs, keylog.Open,
		func(context.Context, proxy.Config) (commandSession, error) {
			t.Fatal("via created owned session")
			return nil, nil
		},
		func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, rfs.Fs, error) {
			return ctx, src, nil
		})
	if pullCode(err) != 0 {
		t.Fatal(err, logs.String())
	}
	assertJSONSummary(t, out.String(), 0, "planned", "none", "none")
	if !reflect.DeepEqual(before, snapshotTree(t, parent)) {
		t.Fatal("via plan mutated destination")
	}
}

func TestPullSMBDryRun(t *testing.T) {
	addr := os.Getenv("SEESTAR_TEST_SAMBA_ADDR")
	if addr == "" {
		t.Skip("container Samba fixture required")
	}
	for _, via := range []bool{false, true} {
		parent := t.TempDir()
		before := snapshotTree(t, parent)
		args := []string{"pull", "--dry-run", "--output=json", "--since=all", "--settle=0"}
		if via {
			args = append(args, "--via="+addr)
		}
		args = append(args, filepath.Join(parent, "missing"))
		s := &pullSessionFixture{addr: addr}
		var out, logs bytes.Buffer
		err := runPull(t.Context(), args, &out, &logs, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) {
			if via {
				t.Fatal("via acquired a device session")
			}
			return s, nil
		})
		var summary map[string]any
		if e := json.Unmarshal(out.Bytes(), &summary, json.MatchCaseInsensitiveNames(true)); e != nil {
			t.Fatal(e, out.String())
		}
		if pullCode(err) != 0 || summary["exit_code"] != float64(0) || summary["status"] != "planned" || summary["planned"] != float64(2) || summary["bytes"] != float64(131072+len("independent literal fixture\n")) {
			t.Fatalf("via=%t summary=%v err=%v logs=%s", via, summary, err, &logs)
		}
		if s.closed != 1-boolInt(via) {
			t.Fatalf("via=%t releases=%d", via, s.closed)
		}
		if !reflect.DeepEqual(before, snapshotTree(t, parent)) {
			t.Fatal("SMB dry-run mutated destination")
		}
	}
}
