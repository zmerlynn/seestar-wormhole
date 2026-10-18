package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
)

func TestPullDirectoryFlags(t *testing.T) {
	for _, dirs := range [][]string{{""}, {"/MyWorks"}, {".."}, {"a//b"}, {`a\b`}, {"a", "a/b"}, {"a", "a"}, {"SHA256SUMS"}, {"NUL"}} {
		args := []string{"pull", "--version"}
		for _, dir := range dirs {
			args = append(args, "--dir="+dir)
		}
		if _, err := parse(args, io.Discard); pullCode(err) != 2 {
			t.Fatalf("dirs %q: err=%v code=%d", dirs, err, pullCode(err))
		}
	}
	o, err := parse([]string{"pull", "--version", "--dir=MyWorks", "--dir=Autorun/Dark", "--dir=comma,name"}, io.Discard)
	if err != nil || len(o.pull.config.Dirs) != 3 {
		t.Fatalf("repeatable flag: %+v %v", o.pull, err)
	}
}

// Use the existing in-process backend seam: the command still owns shutdown,
// release, receipts and checkpoint commit; only the SMB filesystem is simulated.
func TestPullDirectoriesCommand(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-24 * time.Hour)
	for _, name := range []string{"MyWorks/keep/a.fit", "Autorun/Dark/keep/b.fit", "MyWorks/drop/c.fit", "Autorun/Dark/root.fit"} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	var releaseErr error
	run := func(dest string, extra ...string) (int, string, string) {
		t.Helper()
		var out, logs bytes.Buffer
		args := append([]string{"pull", "--settle=0", "--output=json"}, extra...)
		args = append(args, dest)
		s := &pullSessionFixture{closeErr: releaseErr}
		err := runPullWithSource(t.Context(), args, &out, &logs, keylog.Open,
			func(context.Context, proxy.Config) (commandSession, error) { return s, nil },
			func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
				path := root
				if !strings.Contains(strings.Join(extra, " "), "--dir=") {
					path = filepath.Join(root, "MyWorks")
				}
				src, err := local.NewFs(ctx, "simulated-smb", path, configmap.Simple{})
				return ctx, src, err
			})
		return pullCode(err), out.String(), logs.String()
	}
	dest := t.TempDir()
	code, out, logs := run(dest, "--dir=MyWorks", "--dir=Autorun/Dark", "--only=^keep$", "--verify")
	if code != 0 || !strings.Contains(out, `"copied":2`) || !strings.Contains(out, `"verified":2`) {
		t.Fatalf("code=%d out=%s logs=%s", code, out, logs)
	}
	for _, name := range []string{"MyWorks/keep/a.fit", "Autorun/Dark/keep/b.fit"} {
		data, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(data) != name {
			t.Fatalf("%s: data=%s err=%v", name, data, err)
		}
	}
	for _, name := range []string{"MyWorks/drop/c.fit", "Autorun/Dark/root.fit"} {
		if _, err := os.Stat(filepath.Join(dest, name)); !os.IsNotExist(err) {
			t.Fatalf("--only included %s: %v", name, err)
		}
	}
	sums, err := os.ReadFile(filepath.Join(dest, "SHA256SUMS"))
	if err != nil || strings.Count(string(sums), "\n") != 2 {
		t.Fatalf("sums=%s err=%v", sums, err)
	}
	code, out, logs = run(dest, "--dir=MyWorks", "--dir=Autorun/Dark", "--only=^keep$")
	if code != 0 || !strings.Contains(out, `"status":"nothing-new"`) || !strings.Contains(out, `"first_scan":false`) {
		t.Fatalf("repeat: %d %s %s", code, out, logs)
	}
	before, err := os.ReadFile(filepath.Join(dest, ".seestar-wormhole/state.json"))
	if err != nil {
		t.Fatal(err)
	}
	code, out, logs = run(dest, "--dir=MyWorks", "--dir=missing", "--only=^keep$")
	if code != 1 || !strings.Contains(out, `"reason":"missing_directory"`) {
		t.Fatalf("missing: %d %s %s", code, out, logs)
	}
	after, err := os.ReadFile(filepath.Join(dest, ".seestar-wormhole/state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("missing directory changed checkpoints or receipts")
	}
	for _, extra := range [][]string{nil, {"--rebind"}, {"--dry-run"}} {
		code, out, logs = run(dest, extra...)
		if code != 2 || !strings.Contains(out, `"reason":"layout_mismatch"`) {
			t.Fatalf("explicit to default: %d %s %s", code, out, logs)
		}
	}
	legacy := t.TempDir()
	code, out, logs = run(legacy)
	if code != 0 {
		t.Fatalf("legacy: %d %s %s", code, out, logs)
	}
	code, out, logs = run(legacy, "--dir=MyWorks", "--rebind")
	if code != 2 || !strings.Contains(out, `"reason":"layout_mismatch"`) {
		t.Fatalf("default to explicit: %d %s %s", code, out, logs)
	}
	releaseErr = io.ErrUnexpectedEOF
	failed := t.TempDir()
	code, out, logs = run(failed, "--dir=MyWorks", "--dir=Autorun/Dark")
	if code != 1 {
		t.Fatalf("failed release: %d %s %s", code, out, logs)
	}
	failedState, err := pull.OpenReadOnlyDestination(failed)
	if err != nil {
		t.Fatal(err)
	}
	if len(failedState.State.Files) != 4 || len(failedState.State.Selections) != 0 {
		t.Fatalf("release failure state=%+v", failedState.State)
	}
	failedState.Close()
	// Validate the new schema through the public reader, not just in-memory state.
	d, err := pull.OpenReadOnlyDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if len(d.State.Selections) != 2 {
		t.Fatalf("selections=%v", d.State.Selections)
	}
}

func TestPullLayoutBeforeOpen(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, flags := range [][]string{nil, {"--dry-run"}, {"--rebind"}} {
			dest := t.TempDir()
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "MyWorks"), 0700); err != nil {
				t.Fatal(err)
			}
			var setup []string
			if explicit {
				setup = []string{"--dir=MyWorks"}
			}
			if code, out, _, _ := runDirectoryCommand(t, root, dest, setup...); code != 0 {
				t.Fatalf("setup: code=%d out=%s", code, out)
			}
			before, err := os.ReadFile(filepath.Join(dest, ".seestar-wormhole/state.json"))
			if err != nil {
				t.Fatal(err)
			}
			args := append([]string(nil), flags...)
			if !explicit {
				args = append(args, "--dir=MyWorks")
			}
			code, out, sessions, opens := runDirectoryCommand(t, t.TempDir(), dest, args...)
			if code != 2 || !strings.Contains(out, `"reason":"layout_mismatch"`) || sessions != 0 || opens != 0 {
				t.Fatalf("explicit=%v flags=%v code=%d sessions=%d opens=%d out=%s", explicit, flags, code, sessions, opens, out)
			}
			after, err := os.ReadFile(filepath.Join(dest, ".seestar-wormhole/state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("layout refusal changed state")
			}
		}
	}
}
