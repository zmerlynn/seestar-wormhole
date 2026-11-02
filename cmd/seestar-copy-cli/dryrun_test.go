package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
)

// dryRun runs sync --dry-run, leaving the read-only guard off afterward
// for the other tests in this process.
func dryRun(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Cleanup(readonly.Reset)
	return invoke(t, append([]string{"sync", "--endpoint", "h:445", "--dry-run"}, args...)...)
}

// snapshot describes every entry under root: its mode, and for a file its
// size, mtime and SHA-256. A missing root is an empty map.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == root {
			return nil
		}
		if err != nil {
			return err
		}
		fi, err := de.Info()
		if err != nil {
			return err
		}
		desc := fi.Mode().String()
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			desc += " " + fi.ModTime().UTC().Format(time.RFC3339Nano) + " " + hex.EncodeToString(sum[:])
		}
		got[p] = desc
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// A dry run with no destination and no state directory reports what it
// would copy, exits 0, and creates neither. --once with it is allowed.
func TestDryRunCreatesNothing(t *testing.T) {
	dest, stateDir := filepath.Join(t.TempDir(), "dest"), filepath.Join(t.TempDir(), "state")
	old := time.Now().Add(-time.Hour)
	dev := newDevice()
	dev.set("MyWorks/a.fit", "abc", old)
	dev.set("MyWorks/M 31/b.fit", "abcd", old)
	dev.use(t)
	code, stdout, stderr := dryRun(t, "--dest", dest, "--state-dir", stateDir, "--once")
	if code != exitOK || stdout != "" {
		t.Errorf("code=%d stdout=%q, want 0 and no stdout\n%s", code, stdout, stderr)
	}
	for _, want := range []string{
		`level=INFO msg="would copy" path="MyWorks/M 31/b.fit" size=4` + "\n",
		`level=INFO msg="would copy" path=MyWorks/a.fit size=3` + "\n",
		`level=INFO msg="dry run done" copy=2 replace=0 adopt=0 waiting=0 failed=0 skipped=0 bytes=7 elapsed=`,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("log lacks %q:\n%s", want, stderr)
		}
	}
	for _, p := range []string{dest, stateDir} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists after a dry run: %v", p, err)
		}
	}
	if dials, closes := dev.counts(); dials != 1 || closes != 1 {
		t.Errorf("%d sessions dialed and %d closed, want 1 and 1", dials, closes)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the read-only guard is off after a dry run")
			}
		}()
		readonly.Check("a test write")
	}()
}

// populated runs a real pass of a, b and gone into a new destination, then
// changes the device and the destination so that a later pass would leave
// a, replace b, adopt c, copy d, wait for young and leave gone (removed
// locally). It also leaves what a real start repairs: a torn final line in
// the state and a leftover temporary file.
func populated(t *testing.T) (dest, stateDir string) {
	t.Helper()
	dest, stateDir = filepath.Join(t.TempDir(), "dest"), filepath.Join(t.TempDir(), "state")
	old := time.Now().Add(-time.Hour)
	dev := newDevice()
	dev.set("MyWorks/a.fit", "aaa", old)
	dev.set("MyWorks/b.fit", "bbb", old)
	dev.set("MyWorks/gone.fit", "ggg", old)
	dev.use(t)
	if code, _, stderr := invoke(t, "sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", stateDir, "--once"); code != exitOK {
		t.Fatalf("first pass exit %d\n%s", code, stderr)
	}
	if err := os.Remove(filepath.Join(dest, "MyWorks", "gone.fit")); err != nil {
		t.Fatal(err)
	}
	dev.set("MyWorks/b.fit", "bbbb", old.Add(time.Minute))
	dev.set("MyWorks/c.fit", "cc", old)
	place(t, dest, "MyWorks/c.fit", "cc", old)
	dev.set("MyWorks/d.fit", "ddddd", old)
	dev.set("MyWorks/young.fit", "y", time.Now())
	place(t, dest, "MyWorks/"+plan.TempName("MyWorks/d.fit"), "partial", old)
	names, err := filepath.Glob(filepath.Join(stateDir, "sync", "*.jsonl"))
	if err != nil || len(names) != 1 {
		t.Fatalf("state files %q, %v; want one", names, err)
	}
	f, err := os.OpenFile(names[0], os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"path":"MyWorks/torn.fit","si`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return dest, stateDir
}

// A dry run on a destination and state from a real pass changes no byte,
// mode or mtime in either, reports the mix of decisions, and predicts the
// next real pass.
func TestDryRunChangesNothingAndPredicts(t *testing.T) {
	dest, stateDir := populated(t)
	destBefore, stateBefore := snapshot(t, dest), snapshot(t, stateDir)
	code, stdout, stderr := dryRun(t, "--dest", dest, "--state-dir", stateDir)
	if code != exitOK || stdout != "" {
		t.Errorf("code=%d stdout=%q, want 0 and no stdout\n%s", code, stdout, stderr)
	}
	for _, want := range []string{
		`level=WARN msg="ignored the torn final line of the state file"`,
		`level=INFO msg="would replace" path=MyWorks/b.fit size=4` + "\n",
		`level=INFO msg="would adopt" path=MyWorks/c.fit size=2` + "\n",
		`level=INFO msg="would copy" path=MyWorks/d.fit size=5` + "\n",
		`level=INFO msg="would wait" path=MyWorks/young.fit size=1` + "\n",
		`level=INFO msg="dry run done" copy=1 replace=1 adopt=1 waiting=1 failed=0 skipped=0 bytes=9 elapsed=`,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("log lacks %q:\n%s", want, stderr)
		}
	}
	for _, p := range []string{"MyWorks/a.fit", "MyWorks/gone.fit"} {
		if strings.Contains(stderr, "path="+p) {
			t.Errorf("log names %s, which is done:\n%s", p, stderr)
		}
	}
	if got := snapshot(t, dest); !maps.Equal(got, destBefore) {
		t.Errorf("destination changed:\n%v\nwas\n%v", got, destBefore)
	}
	if got := snapshot(t, stateDir); !maps.Equal(got, stateBefore) {
		t.Errorf("state directory changed:\n%v\nwas\n%v", got, stateBefore)
	}

	readonly.Reset()
	code, _, stderr = invoke(t, "sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", stateDir, "--once")
	if want := `msg="pass done" copied=1 replaced=1 adopted=1 waiting=1 failed=0 skipped=0 bytes=9 `; code != exitOK || !strings.Contains(stderr, want) {
		t.Errorf("real pass after the dry run: exit %d, log lacks %q:\n%s", code, want, stderr)
	}
}

// A dry run works when the destination, the state directory and their
// files are read-only.
func TestDryRunReadOnlyDirs(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs permissions that bind the user")
	}
	dest, stateDir := populated(t)
	for _, root := range []string{dest, stateDir} {
		chmodTree(t, root, 0o555, 0o444)
		t.Cleanup(func() { chmodTree(t, root, 0o755, 0o644) })
	}
	destBefore, stateBefore := snapshot(t, dest), snapshot(t, stateDir)
	code, _, stderr := dryRun(t, "--dest", dest, "--state-dir", stateDir)
	if want := `msg="dry run done" copy=1 replace=1 adopt=1 waiting=1 failed=0 skipped=0 bytes=9 `; code != exitOK || !strings.Contains(stderr, want) {
		t.Errorf("exit %d, log lacks %q:\n%s", code, want, stderr)
	}
	if !maps.Equal(snapshot(t, dest), destBefore) || !maps.Equal(snapshot(t, stateDir), stateBefore) {
		t.Error("a read-only tree changed")
	}
}

// chmodTree sets dirMode on every folder and fileMode on every file under
// root, parents first.
func chmodTree(t *testing.T, root string, dirMode, fileMode os.FileMode) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := fileMode
		if de.IsDir() {
			mode = dirMode
		}
		return os.Chmod(p, mode)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A dry run exits 1 when files would fail, as --once does, and when the
// listing fails; it still creates nothing.
func TestDryRunFailures(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name       string
		files      []string
		unreadable []string
		log        []string
	}{
		{name: "file fails", files: []string{"MyWorks/a.fit", "MyWorks/A.fit", "MyWorks/b.fit"},
			log: []string{
				`level=WARN msg="file failed" path=MyWorks/A.fit reason=`,
				`level=WARN msg="file failed" path=MyWorks/a.fit reason=`,
				`level=INFO msg="would copy" path=MyWorks/b.fit size=3`,
				`level=INFO msg="dry run done" copy=1 replace=0 adopt=0 waiting=0 failed=2 skipped=0 bytes=3 `,
				`level=ERROR msg="sync failed" error="failed files: 2"`,
			}},
		{name: "listing fails", files: []string{"MyWorks/sub/a.fit", "Autorun/Dark/a.fit"}, unreadable: []string{"MyWorks/sub"},
			log: []string{
				`level=ERROR msg="dry run failed" copy=0 replace=0 adopt=0 waiting=0 failed=0 skipped=0 bytes=0 elapsed=`,
				`level=ERROR msg="sync failed" error="list the share: readdir MyWorks/sub: permission denied"`,
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest, stateDir := filepath.Join(t.TempDir(), "dest"), filepath.Join(t.TempDir(), "state")
			dev := newDevice()
			for _, p := range tc.files {
				dev.set(p, "abc", old)
			}
			dev.unreadable = tc.unreadable
			dev.use(t)
			code, _, stderr := dryRun(t, "--dest", dest, "--state-dir", stateDir)
			if code != exitFailure {
				t.Errorf("exit %d, want 1\n%s", code, stderr)
			}
			for _, want := range tc.log {
				if !strings.Contains(stderr, want) {
					t.Errorf("log lacks %q:\n%s", want, stderr)
				}
			}
			for _, p := range []string{dest, stateDir} {
				if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("%s exists after a dry run: %v", p, err)
				}
			}
		})
	}
}

// A connection failure exits 1.
func TestDryRunConnectFails(t *testing.T) {
	dev := newDevice()
	dev.dialErrs = []error{errors.New("connection refused")}
	dev.use(t)
	code, _, stderr := dryRun(t, scratch(t)...)
	if want := `error="connect to h:445: connection refused"`; code != exitFailure || !strings.Contains(stderr, want) {
		t.Errorf("exit %d, log lacks %q:\n%s", code, want, stderr)
	}
}

// --dry-run runs one pass, so an explicit --interval is a usage error, even
// at its default value; nothing is dialed.
func TestDryRunRefusesInterval(t *testing.T) {
	dev := newDevice()
	dev.use(t)
	for _, interval := range []string{"1m", "5m"} {
		code, _, stderr := dryRun(t, append(scratch(t), "--interval", interval)...)
		if want := `option=--dry-run`; code != exitUsage || !strings.Contains(stderr, want) {
			t.Errorf("--interval %s: exit %d, log lacks %q:\n%s", interval, code, want, stderr)
		}
	}
	if dials, _ := dev.counts(); dials != 0 {
		t.Errorf("%d sessions dialed, want none", dials)
	}
}
