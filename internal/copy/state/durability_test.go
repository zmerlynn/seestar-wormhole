package state

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// hookSync makes every fsync call fn first for the rest of the test; an error
// from fn fails that fsync.
func hookSync(t *testing.T, fn func(f *os.File) error) {
	t.Helper()
	old := syncFile
	syncFile = func(f *os.File) error {
		if err := fn(f); err != nil {
			return err
		}
		return old(f)
	}
	t.Cleanup(func() { syncFile = old })
}

// hookRename makes every rename call fn first for the rest of the test.
func hookRename(t *testing.T, fn func(from, to string)) {
	t.Helper()
	old := rename
	rename = func(from, to string) error {
		fn(from, to)
		return old(from, to)
	}
	t.Cleanup(func() { rename = old })
}

// isDir reports whether f is a directory.
func isDir(t *testing.T, f *os.File) bool {
	t.Helper()
	fi, err := f.Stat()
	if err != nil {
		t.Fatalf("stat %s: %v", f.Name(), err)
	}
	return fi.IsDir()
}

// checkUnlocked fails unless the lock file of dest and the registration lock
// can be taken right away.
func checkUnlocked(t *testing.T, stateDir, dest string) {
	t.Helper()
	p, _ := Path(stateDir, dest)
	for _, lock := range []string{strings.TrimSuffix(p, ".jsonl") + ".lock", filepath.Join(filepath.Dir(p), registryName)} {
		f, err := lockFile(lock, false)
		if err != nil {
			t.Fatalf("%s was not released: %v", lock, err)
		}
		f.Close()
	}
}

// syncDirEntries lists the names in the sync directory of stateDir.
func syncDirEntries(t *testing.T, stateDir string) []string {
	t.Helper()
	des, err := os.ReadDir(filepath.Join(stateDir, "sync"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	return names
}

func TestOpenPublishesHeaderAfterSync(t *testing.T) {
	stateDir, dest := dirs(t)
	p, _ := Path(stateDir, dest)
	syncDir := filepath.Dir(p)
	var events []string
	var tmp *os.File
	hookSync(t, func(f *os.File) error {
		if isDir(t, f) {
			events = append(events, "sync dir "+f.Name())
			return nil
		}
		tmp = f
		events = append(events, "sync file")
		if filepath.Dir(f.Name()) != syncDir || f.Name() == p {
			t.Errorf("fsync of %s, want a temporary file next to %s", f.Name(), p)
		}
		// The whole header is written before the fsync, and nothing is
		// published yet.
		if got := readFile(t, f.Name()); got != headerLine(dest) {
			t.Errorf("temporary file at fsync = %q, want the header", got)
		}
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("state file exists before the header fsync: %v", err)
		}
		return nil
	})
	hookRename(t, func(from, to string) {
		events = append(events, "rename")
		if tmp == nil || from != tmp.Name() || to != p {
			t.Errorf("rename(%q, %q), want the fsynced temporary file renamed to %q", from, to, p)
		}
		if tmp != nil {
			// Seek, unlike Stat, reports a closed file on Windows too.
			if _, err := tmp.Seek(0, io.SeekCurrent); !errors.Is(err, os.ErrClosed) {
				t.Errorf("temporary file still open at rename: %v", err)
			}
		}
	})
	mustOpen(t, stateDir, dest, nil)
	want := []string{"sync file", "rename", "sync dir " + syncDir, "sync dir " + stateDir}
	if runtime.GOOS == "windows" {
		want = want[:2]
	}
	if !slices.Equal(events, want) {
		t.Errorf("events = %q, want %q", events, want)
	}
	if got := readFile(t, p); got != headerLine(dest) {
		t.Errorf("state file = %q, want the header", got)
	}
}

func TestOpenHeaderSyncFailurePublishesNothing(t *testing.T) {
	stateDir, dest := dirs(t)
	p, _ := Path(stateDir, dest)
	hookSync(t, func(f *os.File) error {
		if !isDir(t, f) {
			return errors.New("injected header fsync failure")
		}
		return nil
	})
	hookRename(t, func(from, to string) { t.Errorf("rename(%q, %q) after a failed fsync", from, to) })
	s, err := Open(stateDir, dest, nil)
	if err == nil {
		s.Close()
		t.Fatal("Open succeeded, want the fsync error")
	}
	if !strings.Contains(err.Error(), "injected header fsync failure") || !strings.Contains(err.Error(), p) {
		t.Errorf("Open error = %q, want the fsync error and the state file", err)
	}
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("state file published after a failed header fsync: %v", err)
	}
	// The temporary file is gone; only the locks are left.
	for _, name := range syncDirEntries(t, stateDir) {
		if !strings.HasSuffix(name, ".lock") {
			t.Errorf("%s left in the sync directory", name)
		}
	}
	checkUnlocked(t, stateDir, dest)
}

func TestOpenDirSyncFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directories are not fsynced on Windows")
	}
	for _, which := range []string{"sync", "state"} {
		t.Run(which, func(t *testing.T) {
			stateDir, dest := dirs(t)
			failDir := stateDir
			if which == "sync" {
				failDir = filepath.Join(stateDir, "sync")
			}
			hookSync(t, func(f *os.File) error {
				if f.Name() == failDir {
					return errors.New("injected directory fsync failure")
				}
				return nil
			})
			s, err := Open(stateDir, dest, nil)
			if err == nil {
				s.Close()
				t.Fatalf("Open succeeded, want the fsync error of %s", failDir)
			}
			if !strings.Contains(err.Error(), "injected directory fsync failure") {
				t.Errorf("Open error = %q, want the directory fsync error", err)
			}
			checkUnlocked(t, stateDir, dest)
		})
	}
}

func TestOpenSyncsRecoveryTruncate(t *testing.T) {
	for _, fail := range []bool{false, true} {
		stateDir, dest := dirs(t)
		good := headerLine(dest) + lineDark
		p := writeState(t, stateDir, dest, good+`{"path":"torn`)
		calls := 0
		hookSync(t, func(f *os.File) error {
			if f.Name() != p {
				return nil
			}
			calls++
			// The fsync follows the truncate to the end of the last good line.
			if fi, err := f.Stat(); err != nil || fi.Size() != int64(len(good)) {
				t.Errorf("fail=%v: size at the recovery fsync = %v (%v), want %d", fail, fi.Size(), err, len(good))
			}
			if fail {
				return errors.New("injected recovery fsync failure")
			}
			return nil
		})
		s, err := Open(stateDir, dest, nil)
		if calls != 1 {
			t.Errorf("fail=%v: %d fsyncs of the state file, want 1", fail, calls)
		}
		if !fail {
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			s.Close()
			continue
		}
		if err == nil {
			s.Close()
			t.Fatal("Open succeeded, want the recovery fsync error")
		}
		if !strings.Contains(err.Error(), "truncate the torn final line: injected recovery fsync failure") {
			t.Errorf("Open error = %q, want the recovery fsync error", err)
		}
		checkUnlocked(t, stateDir, dest)
	}
}

// TestOpenIgnoresOldTempFile checks that whatever is at the temporary name an
// earlier release used is neither followed nor published.
func TestOpenIgnoresOldTempFile(t *testing.T) {
	const precious = "precious bytes\n"
	for _, kind := range []string{"symlink", "hard link", "loose file"} {
		t.Run(kind, func(t *testing.T) {
			stateDir, dest := dirs(t)
			p, _ := Path(stateDir, dest)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			victim := filepath.Join(t.TempDir(), "victim")
			if err := os.WriteFile(victim, []byte(precious), 0o644); err != nil {
				t.Fatal(err)
			}
			old := p + ".tmp"
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(victim, old)
			case "hard link":
				err = os.Link(victim, old)
			default:
				err = os.WriteFile(old, []byte(precious), 0o666)
				if err == nil {
					err = os.Chmod(old, 0o666)
				}
			}
			if err != nil {
				t.Skipf("cannot make a %s: %v", kind, err)
			}
			mustOpen(t, stateDir, dest, nil)
			if got := readFile(t, victim); got != precious {
				t.Errorf("unrelated file changed to %q", got)
			}
			if got := readFile(t, old); got != precious {
				t.Errorf("old temporary name now holds %q", got)
			}
			if got := readFile(t, p); got != headerLine(dest) {
				t.Errorf("state file = %q, want the header", got)
			}
			if runtime.GOOS != "windows" {
				if fi, err := os.Lstat(p); err != nil || fi.Mode() != 0o600 {
					t.Errorf("state file mode = %v (%v), want -rw-------", fi.Mode(), err)
				}
			}
		})
	}
}
