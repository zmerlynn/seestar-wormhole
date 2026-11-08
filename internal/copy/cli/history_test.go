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

	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// Without --history-dir, sync uses the folder seestar-copy-cli used, so an
// upgrade keeps its history and its lock. The expected folder is written
// out here, not taken from state.DefaultDir.
func TestLegacyHistoryRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	var config string
	switch runtime.GOOS {
	case "linux":
		config = filepath.Join(home, ".config")
	case "darwin":
		config = filepath.Join(home, "Library", "Application Support")
	case "windows":
		config = filepath.Join(home, "AppData", "Roaming")
		t.Setenv("AppData", config)
	default:
		t.Skipf("no literal config folder for %s", runtime.GOOS)
	}
	root := filepath.Join(config, "seestar-copy")

	// A history in today's format records a file that was copied and has
	// since been moved out of the destination.
	const moved = "MyWorks/moved.fit"
	mtime := time.Date(2026, 11, 1, 5, 4, 5, 0, time.UTC)
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := state.Path(root, dest)
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

	// While another copy holds that folder's lock, sync stops.
	lock, err := lockState(t, root, dest)
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr = invoke(t, args...)
	lock.Close()
	if code != exitFailure || !strings.Contains(stderr, state.ErrLocked.Error()) {
		t.Errorf("with the lock held: exit %d, want 1:\n%s", code, stderr)
	}

	// No other history folder appeared.
	entries, err := os.ReadDir(config)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !slices.Equal(names, []string{"seestar-copy"}) {
		t.Errorf("config folder holds %q, want only seestar-copy", names)
	}
}
