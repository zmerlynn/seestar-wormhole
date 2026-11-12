package app

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/engine"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// Clear history deletes the saved folder's state file, under its lock, and
// sends a Free up space preview back to Start.
func TestClearHistory(t *testing.T) {
	clear := func(e *appEnv) (int, string) {
		w := e.post("/copy/api/clear", `{}`)
		return w.Code, w.Body.String()
	}
	if code, body := clear(newAppEnv(t, nil)); code != 409 || !strings.Contains(body, "save the copy settings first") {
		t.Errorf("before settings are saved: %d %s", code, body)
	}

	folder := t.TempDir()
	e := newAppEnv(t, savedAt(folder))
	// A sync of the folder holds its lock.
	st, err := state.Open(e.s.stateDir, folder, nil)
	if err != nil {
		t.Fatal(err)
	}
	file, _ := state.Path(e.s.stateDir, folder)
	if code, body := clear(e); code != 409 || !strings.Contains(body, "another copy is using this folder, in another seestar-wormhole window or seestar-wormhole-cli sync.") {
		t.Errorf("while another copy holds the lock: %d %s", code, body)
	}
	st.Close()
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("a refused clear removed the state file: %v", err)
	}

	// Not while a check or a delete runs; the preview it shows stays.
	e.s.free.done = make(chan struct{})
	e.s.free.st = freeStatus{state: frChecking}
	if code, body := clear(e); code != 409 || !strings.Contains(body, "wait for Free up space to finish") {
		t.Errorf("while Free up space runs: %d %s", code, body)
	}
	e.s.free.done = nil
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("a refused clear removed the state file: %v", err)
	}

	e.s.free.st = freeStatus{state: frPreview, id: 3, preview: &engine.DeleteResult{}}
	if code, body := clear(e); code != 200 {
		t.Fatalf("clear: %d %s", code, body)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("state file after clear: %v", err)
	}
	if v := e.s.free.view(); v.State != frStart || !v.Changed {
		t.Errorf("Free up space after clear: %+v", v)
	}
	// A clear with no history left is fine.
	if code, body := clear(e); code != 200 {
		t.Errorf("second clear: %d %s", code, body)
	}
}

// Clear history is refused while copying is on.
func TestClearHistoryWhileCopying(t *testing.T) {
	e := newAppEnv(t, savedAt(t.TempDir()))
	if w := e.post("/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	if w := e.post("/copy/api/clear", `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), "stop copying first") {
		t.Errorf("while copying: %d %s", w.Code, w.Body)
	}
}

// oldHistoryEnv sets the per-user config folder to a new temporary one
// holding the old default history folder, seestar-copy, with a history for
// a new folder, and returns an app whose default history folder is
// state.DefaultDir, with the folder saved, the config folder, and the old
// state file.
func oldHistoryEnv(t *testing.T) (e *appEnv, folder, base, old string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("AppData", home)
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	folder = t.TempDir()
	st, err := state.Open(filepath.Join(base, "seestar-copy"), folder, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if old, err = state.Path(filepath.Join(base, "seestar-copy"), folder); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal(err)
	}
	def, err := state.DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	return newAppEnvIn(t, savedAt(folder), def), folder, base, old
}

// configNames is what the config folder holds.
func configNames(t *testing.T, base string) []string {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// While only the old default history folder exists, the page and a
// preview read it in place and create nothing; Copy moves it once.
func TestOldHistoryMovesOnCopy(t *testing.T) {
	e, folder, base, old := oldHistoryEnv(t)
	if got := decode[struct {
		History string `json:"history"`
	}](t, e.serve("GET", "/copy/api/status", "")).History; got != old {
		t.Errorf("history shown %q, want the old one %q", got, old)
	}
	if w := e.post("/copy/api/copy", `{"action":"preview"}`); w.Code != 200 {
		t.Fatalf("preview = %d %s", w.Code, w.Body)
	}
	for deadline := time.Now().Add(10 * time.Second); e.s.preview.running(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the preview did not end")
		}
	}
	if got := configNames(t, base); !slices.Equal(got, []string{"seestar-copy"}) {
		t.Errorf("after a preview the config folder holds %q, want only seestar-copy", got)
	}

	if w := e.post("/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	e.s.copy.stop()
	for deadline := time.Now().Add(10 * time.Second); e.m.Busy(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("copying did not stop")
		}
	}
	if got := configNames(t, base); !slices.Equal(got, []string{"seestar-wormhole"}) {
		t.Errorf("after Copy the config folder holds %q, want only seestar-wormhole", got)
	}
	moved, _ := state.Path(e.s.stateDir, folder)
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("the moved state file: %v", err)
	}
	if got := decode[struct {
		History string `json:"history"`
	}](t, e.serve("GET", "/copy/api/status", "")).History; got != moved {
		t.Errorf("history shown %q, want %q", got, moved)
	}
}

// Clear history moves the old default folder before it clears, so the old
// history can't come back with a later move.
func TestOldHistoryMovesOnClear(t *testing.T) {
	e, folder, base, _ := oldHistoryEnv(t)
	if w := e.post("/copy/api/clear", `{}`); w.Code != 200 {
		t.Fatalf("clear = %d %s", w.Code, w.Body)
	}
	if got := configNames(t, base); !slices.Equal(got, []string{"seestar-wormhole"}) {
		t.Errorf("after a clear the config folder holds %q, want only seestar-wormhole", got)
	}
	file, _ := state.Path(e.s.stateDir, folder)
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("state file after clear: %v", err)
	}
}

// A move that fails stops Copy, Free up space and Clear history, says what
// to do, and logs a warning; the old folder is left as it was.
func TestOldHistoryMoveFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the settings folder's permissions are checked only on Unix")
	}
	e, _, base, old := oldHistoryEnv(t)
	// A settings folder that isn't private stops the move.
	if err := os.Mkdir(filepath.Join(base, "seestar-wormhole"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(base, "seestar-wormhole"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, try := range []struct{ path, body string }{
		{"/copy/api/copy", `{"action":"start"}`},
		{"/copy/api/delete", `{"action":"find","days":"0","trust":false}`},
		{"/copy/api/clear", `{}`},
	} {
		if w := e.post(try.path, try.body); w.Code != 409 || !strings.Contains(w.Body.String(), "move the folder by hand") {
			t.Errorf("%s %s = %d %s", try.path, try.body, w.Code, w.Body)
		}
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("the old state file: %v", err)
	}
	if logs := e.logs.String(); strings.Count(logs, `level=WARN msg="move the copy history"`) != 3 {
		t.Errorf("the log has no warning for each failed move:\n%s", logs)
	}
}
