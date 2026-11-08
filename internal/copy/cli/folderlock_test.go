package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// holdFolder takes dest's folder lock, as another writer would, until the
// test ends.
func holdFolder(t *testing.T, dest string) {
	t.Helper()
	r, err := os.OpenRoot(dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	l, err := state.LockFolder(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
}

// sync and delete --force into a folder another writer holds fail, exit 1,
// before they connect, naming the folder and its lock file.
func TestFolderBusy(t *testing.T) {
	s := newWritableShare(t)
	s.put(t, "MyWorks/M 31/a.fit", "aaa", deleteOld)
	s.use(t)
	dest, stateDir := t.TempDir(), t.TempDir()
	if code, _, stderr := invoke(t, "sync", "--endpoint", "h:445", "--dest", dest, "--history-dir", stateDir, "--once"); code != exitOK {
		t.Fatalf("sync: code=%d:\n%s", code, stderr)
	}
	holdFolder(t, dest)
	s.last = nil
	want := `error="destination ` + dest + `: another copy or delete is using this folder (lock file ` + filepath.Join(dest, plan.LockName) + `)"`
	code, _, stderr := invoke(t, "sync", "--endpoint", "h:445", "--dest", dest, "--history-dir", stateDir, "--once")
	if code != exitFailure || !strings.Contains(stderr, `level=ERROR msg="sync failed" `+want) || s.last != nil {
		t.Errorf("sync: code=%d, dialed %v:\n%s\nwant exit 1 with %s and no dial", code, s.last != nil, stderr, want)
	}
	code, _, stderr = deleteRun(t, "--force", "--dest", dest, "--history-dir", stateDir)
	if code != exitFailure || !strings.Contains(stderr, `level=ERROR msg="delete failed" `+want) || s.last != nil {
		t.Errorf("delete: code=%d, dialed %v:\n%s\nwant exit 1 with %s and no dial", code, s.last != nil, stderr, want)
	}
	if !s.has(t, "MyWorks/M 31/a.fit") {
		t.Error("a.fit was deleted")
	}
}

// delete --ignore-state has no local folder: it takes no lock and creates
// nothing, not even in the working directory.
func TestDeleteIgnoreStateTakesNoLock(t *testing.T) {
	s := newWritableShare(t)
	s.put(t, "Video/a.mp4", "aaa", deleteOld)
	s.use(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	code, _, stderr := deleteRun(t, "--force", "--ignore-state", "--include", "*.mp4")
	if code != exitOK || s.has(t, "Video/a.mp4") {
		t.Fatalf("code=%d, a.mp4 still there %v:\n%s", code, s.has(t, "Video/a.mp4"), stderr)
	}
	if ents, err := os.ReadDir(cwd); err != nil || len(ents) != 0 {
		t.Errorf("working directory: %v, %v, want it empty", ents, err)
	}
}
