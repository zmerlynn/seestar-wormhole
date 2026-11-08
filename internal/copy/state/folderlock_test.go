package state

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
)

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	r, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// The folder lock is an empty file of mode 0600 at the top of the folder,
// exclusive while held and kept after it is released.
func TestLockFolder(t *testing.T) {
	dir := t.TempDir()
	first, err := LockFolder(openRoot(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, plan.LockName)
	fi, err := os.Lstat(name)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != 0 || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("lock file: %v, %v, want an empty regular file of mode 0600", fi, err)
	}
	_, err = LockFolder(openRoot(t, dir))
	if !errors.Is(err, ErrFolderLocked) {
		t.Fatalf("second lock while held: %v, want ErrFolderLocked", err)
	}
	if want := "destination " + dir + ": another copy or delete is using this folder (lock file " + name + ")"; err.Error() != want {
		t.Errorf("got %q, want %q", err, want)
	}
	first.Close()
	again, err := LockFolder(openRoot(t, dir))
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again.Close()
	if !exists(name) {
		t.Error("the lock file was removed")
	}
}

// A lock file that is not a regular file fails, naming it.
func TestLockFolderNotRegular(t *testing.T) {
	for _, kind := range []string{"folder", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, plan.LockName)
			var err error
			if kind == "folder" {
				err = os.Mkdir(name, 0o755)
			} else {
				err = os.Symlink(filepath.Join(t.TempDir(), "target"), name)
			}
			if err != nil {
				t.Skipf("make a %s: %v", kind, err)
			}
			_, err = LockFolder(openRoot(t, dir))
			if err == nil || !strings.Contains(err.Error(), "lock file "+name+": not a regular file") || !strings.HasSuffix(err.Error(), "; remove it") {
				t.Errorf("got %v, want a not-a-regular-file error naming %s", err, name)
			}
		})
	}
}

// A folder removed after its root was opened fails with fs.ErrNotExist,
// and the lock file is not created anywhere.
func TestLockFolderRemoved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to remove an open folder")
	}
	dir := filepath.Join(t.TempDir(), "dest")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r := openRoot(t, dir)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := LockFolder(r); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("got %v, want fs.ErrNotExist", err)
	}
	if exists(dir) {
		t.Error("the folder came back")
	}
}

func exists(name string) bool {
	_, err := os.Lstat(name)
	return err == nil
}
