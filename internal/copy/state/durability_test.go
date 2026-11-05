package state

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
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

// hookWrite makes every writeFile call fn first for the rest of the test; an
// error from fn fails that write, which then publishes nothing.
func hookWrite(t *testing.T, fn func(dir *os.File, name string, b []byte, p privatefile.Policy) error) {
	t.Helper()
	old := writeFile
	writeFile = func(dir *os.File, name string, b []byte, p privatefile.Policy) error {
		if err := fn(dir, name, b, p); err != nil {
			return err
		}
		return old(dir, name, b, p)
	}
	t.Cleanup(func() { writeFile = old })
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

func TestOpenPublishesHeaderAtomically(t *testing.T) {
	stateDir, dest := dirs(t)
	p, _ := Path(stateDir, dest)
	syncDir := filepath.Dir(p)
	var events []string
	hookSync(t, func(f *os.File) error {
		if isDir(t, f) {
			events = append(events, "sync dir "+f.Name())
		}
		return nil
	})
	hookWrite(t, func(dir *os.File, name string, b []byte, policy privatefile.Policy) error {
		events = append(events, "write")
		// A protected DACL would refuse a state directory on FAT32 or exFAT
		// and change the ACLs the reference promises.
		if policy != privatefile.Inherited {
			t.Errorf("writeFile policy = %v, want Inherited", policy)
		}
		if dir.Name() != syncDir || name != filepath.Base(p) {
			t.Errorf("writeFile(%q, %q), want %q in %q", dir.Name(), name, filepath.Base(p), syncDir)
		}
		if string(b) != headerLine(dest) {
			t.Errorf("writeFile content = %q, want the header", b)
		}
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("state file exists before the header write: %v", err)
		}
		return nil
	})
	mustOpen(t, stateDir, dest, nil)
	want := []string{"write", "sync dir " + stateDir}
	if runtime.GOOS == "windows" {
		want = want[:1]
	}
	if !slices.Equal(events, want) {
		t.Errorf("events = %q, want %q", events, want)
	}
	if got := readFile(t, p); got != headerLine(dest) {
		t.Errorf("state file = %q, want the header", got)
	}
}

func TestOpenHeaderWriteFailurePublishesNothing(t *testing.T) {
	stateDir, dest := dirs(t)
	p, _ := Path(stateDir, dest)
	hookWrite(t, func(*os.File, string, []byte, privatefile.Policy) error {
		return errors.New("injected header write failure")
	})
	s, err := Open(stateDir, dest, nil)
	if err == nil {
		s.Close()
		t.Fatal("Open succeeded, want the write error")
	}
	if !strings.Contains(err.Error(), "injected header write failure") || !strings.Contains(err.Error(), p) {
		t.Errorf("Open error = %q, want the write error and the state file", err)
	}
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("state file published after a failed header write: %v", err)
	}
	// Only the locks are left.
	for _, name := range syncDirEntries(t, stateDir) {
		if !strings.HasSuffix(name, ".lock") {
			t.Errorf("%s left in the sync directory", name)
		}
	}
	checkUnlocked(t, stateDir, dest)
}

// The fsync of the sync directory belongs to privatefile.WriteFile; Open
// fsyncs the state directory above it, which may be new too.
func TestOpenDirSyncFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directories are not fsynced on Windows")
	}
	stateDir, dest := dirs(t)
	hookSync(t, func(f *os.File) error {
		if f.Name() == stateDir {
			return errors.New("injected directory fsync failure")
		}
		return nil
	})
	s, err := Open(stateDir, dest, nil)
	if err == nil {
		s.Close()
		t.Fatalf("Open succeeded, want the fsync error of %s", stateDir)
	}
	if !strings.Contains(err.Error(), "injected directory fsync failure") {
		t.Errorf("Open error = %q, want the directory fsync error", err)
	}
	checkUnlocked(t, stateDir, dest)
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
