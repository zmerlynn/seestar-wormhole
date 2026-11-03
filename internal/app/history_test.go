package app

import (
	"os"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// Clear history deletes the saved folder's state file, under its lock, and
// sends a Free up space preview back to Start.
func TestClearHistory(t *testing.T) {
	clear := func(e *appEnv) (int, string) {
		w := e.post("/app/api/clear", `{}`)
		return w.Code, w.Body.String()
	}
	if code, body := clear(newAppEnv(t, "")); code != 409 || !strings.Contains(body, "set the telescope and folder in Settings first") {
		t.Errorf("before settings are saved: %d %s", code, body)
	}

	folder := t.TempDir()
	e := newAppEnv(t, `{"version":1,"address":"192.0.2.10:445","folder":`+quote(folder)+`}`)
	// A sync of the folder holds its lock.
	st, err := state.Open(e.s.stateDir, folder, nil)
	if err != nil {
		t.Fatal(err)
	}
	file, _ := state.Path(e.s.stateDir, folder)
	if code, body := clear(e); code != 409 || !strings.Contains(body, "another copy is using this folder") {
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
	e := newAppEnv(t, `{"version":1,"address":"192.0.2.10:445","folder":`+quote(t.TempDir())+`}`)
	if w := e.post("/app/api/copy", `{"action":"start"}`); w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	if w := e.post("/app/api/clear", `{}`); w.Code != 409 || !strings.Contains(w.Body.String(), "stop copying first") {
		t.Errorf("while copying: %d %s", w.Code, w.Body)
	}
}
