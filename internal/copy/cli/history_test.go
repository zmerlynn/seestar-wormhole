package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// configFolder sets the per-user config folder to a new temporary folder
// and returns it, written out here, not taken from os.UserConfigDir.
func configFolder(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	switch runtime.GOOS {
	case "linux":
		return filepath.Join(home, ".config")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support")
	case "windows":
		config := filepath.Join(home, "AppData", "Roaming")
		t.Setenv("AppData", config)
		return config
	}
	t.Skipf("no literal config folder for %s", runtime.GOOS)
	return ""
}

// oldHistory writes a history in the old default folder, seestar-copy, in
// today's format, for dest, recording moved as copied, and returns the
// history file's path.
func oldHistory(t *testing.T, config, dest, moved string, mtime time.Time) string {
	t.Helper()
	p, err := state.Path(filepath.Join(config, "seestar-copy"), dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	header := `{"v":1,"dest":` + strconv.Quote(dest) + "}\n"
	record := `{"path":"` + moved + `","size":3,"mtime":"` + mtime.Format(time.RFC3339Nano) + `","sha256":"` + strings.Repeat("0", 64) +
		`","at":"2026-11-01T06:00:00Z","how":"copied"}` + "\n"
	if err := os.WriteFile(p, []byte(header+record), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// names returns the names in dir.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Without --history-dir, the first sync moves the history the old
// seestar-copy-cli kept in the per-user config folder's seestar-copy to
// seestar-wormhole/copy-history, and copies nothing again. The folders are
// written out here, not taken from state.DefaultDir.
func TestOldHistoryMoves(t *testing.T) {
	config := configFolder(t)
	root := filepath.Join(config, "seestar-wormhole", "copy-history")

	// The history records a file that was copied and has since been moved
	// out of the destination.
	const moved = "MyWorks/moved.fit"
	mtime := time.Date(2026, 11, 1, 5, 4, 5, 0, time.UTC)
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	old := oldHistory(t, config, dest, moved, mtime)
	saved := readFile(t, old)
	dev := newDevice()
	dev.use(t)
	dev.set(moved, "abc", mtime)
	args := []string{"sync", "--endpoint", "127.0.0.1:4445", "--dest", dest, "--once"}

	code, _, stderr := invoke(t, args...)
	dev.mu.Lock()
	opens := dev.opens
	dev.mu.Unlock()
	if code != exitOK || opens != 0 {
		t.Errorf("exit %d after %d reads, want 0 and none:\n%s", code, opens, stderr)
	}
	if _, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(moved))); !os.IsNotExist(err) {
		t.Errorf("the moved file was copied again: %v", err)
	}
	if !strings.Contains(stderr, "moved the copy history") || !strings.Contains(stderr, root) {
		t.Errorf("no info record of the move to %s:\n%s", root, stderr)
	}
	if !slices.Equal(names(t, config), []string{"seestar-wormhole"}) {
		t.Errorf("config folder holds %q, want only seestar-wormhole", names(t, config))
	}
	p, err := state.Path(root, dest)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); !strings.HasPrefix(got, saved) {
		t.Errorf("moved history is %q, want it to start with %q", got, saved)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Lstat(filepath.Dir(root)); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("seestar-wormhole is %v, %v, want 0700", fi, err)
		}
	}

	// The next sync uses the moved folder: while another copy holds its
	// lock, sync stops.
	lock, err := lockState(t, root, dest)
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr = invoke(t, args...)
	lock.Close()
	if code != exitFailure || !strings.Contains(stderr, state.ErrLocked.Error()) {
		t.Errorf("with the lock held: exit %d, want 1:\n%s", code, stderr)
	}
}

// A read-only run reads the old folder in place while only it exists, and
// creates, moves or changes nothing: find, browse's first read, and dry
// runs of sync and delete.
func TestOldHistoryReadInPlace(t *testing.T) {
	t.Cleanup(readonly.Reset)
	config := configFolder(t)
	const moved = "MyWorks/moved.fit"
	mtime := time.Date(2026, 11, 1, 5, 4, 5, 0, time.UTC)
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	old := oldHistory(t, config, dest, moved, mtime)
	saved := readFile(t, old)
	dev := newDevice()
	dev.use(t)
	dev.set(moved, "abc", mtime)

	// find gives the file the status the old history records.
	code, stdout, stderr := invoke(t, "find", "--endpoint", "127.0.0.1:4445", "--dest", dest, "--format", "jsonl")
	readonly.Reset()
	if code != exitOK || !strings.Contains(stdout, `"status":"copied"`) {
		t.Errorf("find: exit %d, want 0 and %s copied:\n%s%s", code, moved, stdout, stderr)
	}
	for _, args := range [][]string{
		{"sync", "--endpoint", "127.0.0.1:4445", "--dest", dest, "--once", "--dry-run"},
		{"delete", "--endpoint", "127.0.0.1:4445", "--dest", dest, "--dry-run"},
	} {
		code, _, stderr = invoke(t, args...)
		readonly.Reset()
		if code != exitOK || strings.Contains(stderr, "moved the copy history") {
			t.Errorf("%s: exit %d, want 0 and no move:\n%s", args[0], code, stderr)
		}
	}
	if _, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(moved))); !os.IsNotExist(err) {
		t.Errorf("a dry run copied: %v", err)
	}
	if !slices.Equal(names(t, config), []string{"seestar-copy"}) {
		t.Errorf("config folder holds %q, want only seestar-copy", names(t, config))
	}
	if got := readFile(t, old); got != saved {
		t.Errorf("old history is %q, want %q unchanged", got, saved)
	}
}

// With --history-dir, nothing moves, even when the default folder is
// missing and the old one exists.
func TestOldHistoryStaysWithHistoryDir(t *testing.T) {
	config := configFolder(t)
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 11, 1, 5, 4, 5, 0, time.UTC)
	old := oldHistory(t, config, dest, "MyWorks/moved.fit", mtime)
	saved := readFile(t, old)
	dev := newDevice()
	dev.use(t)
	explicit := filepath.Join(t.TempDir(), "history")
	code, _, stderr := invoke(t, "sync", "--endpoint", "127.0.0.1:4445", "--dest", dest, "--once", "--history-dir", explicit)
	if code != exitOK {
		t.Fatalf("exit %d:\n%s", code, stderr)
	}
	if !slices.Equal(names(t, config), []string{"seestar-copy"}) || readFile(t, old) != saved {
		t.Errorf("config folder holds %q, want only seestar-copy, unchanged", names(t, config))
	}
	if _, err := os.Lstat(explicit); err != nil {
		t.Errorf("the explicit folder was not used: %v", err)
	}
}

// An open browse follows the history to its new folder when another
// process moves it, and keeps reading the new history when an older
// installation then makes the old folder again: a record added to the new
// history shows on the next listing.
func TestOldHistoryBrowseFollowsMove(t *testing.T) {
	config := configFolder(t)
	dest := t.TempDir()
	oldRoot := filepath.Join(config, "seestar-copy")
	writeFindState(t, dest, oldRoot)
	statusDevice(t)
	b := startBrowse(t, "--dest", dest)
	if code, body := b.get(t, "api/list?path=MyWorks"); code != 200 || !strings.Contains(body, `"status":"copied"`) {
		t.Fatalf("list with the old history: %d %s", code, body)
	}
	if _, err := os.Stat(oldRoot); err != nil {
		t.Fatalf("browse moved the old history: %v", err)
	}
	// Another process's move, outside this one's read-only guard.
	def := filepath.Join(config, "seestar-wormhole", "copy-history")
	if err := os.MkdirAll(filepath.Dir(def), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldRoot, def); err != nil {
		t.Fatal(err)
	}
	writeFindState(t, dest, oldRoot)
	if code, body := b.get(t, "api/list?path=MyWorks"); code != 200 {
		t.Fatalf("list with both folders: %d %s", code, body)
	}
	path, err := state.Path(def, dest)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"path":"MyWorks/d.fit","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","sha256":"` + strings.Repeat("0", 64) + `","at":"2026-11-01T07:00:00Z","how":"copied"}` + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	want := `"path":"MyWorks/d.fit","type":"file","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","status":"copied"`
	if code, body := b.get(t, "api/list?path=MyWorks"); code != 200 || !strings.Contains(body, want) {
		t.Errorf("list after the new history's record: %d %s", code, body)
	}
	if code := b.end(); code != 0 {
		t.Errorf("browse exit %d", code)
	}
}
