package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// historyShare is a share folder holding paths, each old enough to free.
func historyShare(t *testing.T, paths ...string) string {
	t.Helper()
	share := t.TempDir()
	for _, p := range paths {
		name := filepath.Join(share, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("abc"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, ancient, ancient); err != nil {
			t.Fatal(err)
		}
	}
	return share
}

// historySave is a Save of folder with history as its copy history folder,
// copying once.
func historySave(folder, history string) string {
	return `{"folder":` + quote(folder) + `,"history_dir":` + quote(history) + `,"interval":"5","files_at_once":"2","excludes":[],"includes":[],"skip_built_in":true,"once":true}`
}

// copyOnce saves folder and history, runs one pass and waits for it.
func (e *appEnv) copyOnce(folder, history string) copyView {
	e.t.Helper()
	if w := e.post("/copy/api/settings", historySave(folder, history)); w.Code != 200 {
		e.t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if w := e.post("/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
		e.t.Fatalf("start copying: %d %s", w.Code, w.Body)
	}
	return e.waitCopy(stDone, stCantWrite, stInUse)
}

// waitCopy waits until copying is off in one of states.
func (e *appEnv) waitCopy(states ...string) copyView {
	e.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		v := e.s.copy.view(time.Now())
		if !v.On && slices.Contains(states, v.State) {
			return v
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("copying: %+v, want off in %q", v, states)
		}
	}
}

// freeFind runs a Free up space check and returns the folders it offers.
func (e *appEnv) freeFind(trust bool) []string {
	e.t.Helper()
	body := `{"action":"find","days":"0","trust":false}`
	if trust {
		body = `{"action":"find","days":"0","trust":true}`
	}
	if w := e.post("/copy/api/delete", body); w.Code != 200 {
		e.t.Fatalf("find: %d %s", w.Code, w.Body)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		v := e.s.free.view()
		switch v.State {
		case frPreview, frNothing:
			var folders []string
			for _, g := range v.Groups {
				folders = append(folders, g.Folder)
			}
			return folders
		case frCheckFail:
			e.t.Fatalf("find failed: %+v", v)
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("find: %+v", v)
		}
	}
}

// copyPreview runs Copy's preview and returns the folders it would copy.
func (e *appEnv) copyPreview() []string {
	e.t.Helper()
	if w := e.post("/copy/api/copy", `{"action":"preview"}`); w.Code != 200 {
		e.t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		v := e.s.preview.view()
		if v != nil && v.State == pvDone {
			var folders []string
			for _, g := range v.Groups {
				folders = append(folders, g.Folder)
			}
			return folders
		}
		if v != nil && v.State == pvFailed || time.Now().After(deadline) {
			e.t.Fatalf("preview: %+v", v)
		}
	}
}

// A chosen copy history folder holds the pass's history, and nothing is
// written under the default one; the page shows its state file.
func TestCopyHistoryDirChosen(t *testing.T) {
	share := historyShare(t, "MyWorks/a.jpg")
	folder, history := filepath.Join(t.TempDir(), "Seestar"), filepath.Join(t.TempDir(), "history")
	e := newAppEnv(t, nil)
	e.d.dir = share
	if v := e.copyOnce(folder, history); v.State != stDone {
		t.Fatalf("copy once: %+v", v)
	}
	records, err := state.Load(history, folder, nil)
	if _, ok := records["MyWorks/a.jpg"]; err != nil || !ok {
		t.Errorf("the chosen history's records = %v, %v", records, err)
	}
	if entries, err := os.ReadDir(e.s.stateDir); err != nil || len(entries) != 0 {
		t.Errorf("the default history folder holds %v, %v", entries, err)
	}
	got := decode[struct {
		Settings       Settings `json:"settings"`
		History        string   `json:"history"`
		DefaultHistory string   `json:"default_history"`
	}](t, e.serve("GET", "/copy/api/status", ""))
	want, _ := state.Path(history, folder)
	if got.Settings.HistoryDir != history || got.History != want || got.DefaultHistory != e.s.stateDir {
		t.Errorf("status = %+v; want history_dir %s, history %s, default %s", got, history, want, e.s.stateDir)
	}
	if saves := e.saves(); len(saves) != 1 || saves[0].HistoryDir != history {
		t.Errorf("saved %+v", saves)
	}
}

// A history folder that isn't a full path, or that is inside the images
// folder by its spelling, isn't saved. One that leads inside only through
// a symlink is saved, and copying refuses it at its start.
func TestCopyHistoryDirRefused(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "Seestar")
	for name, history := range map[string]string{
		"relative": "history", "tilde": "~/history", "inside": filepath.Join(folder, "history"),
		"the folder": folder, "spelled inside": folder + string(filepath.Separator) + "x" + string(filepath.Separator) + "..",
	} {
		e := newAppEnv(t, nil)
		w := e.post("/copy/api/settings", historySave(folder, history))
		if got := decode[fieldErrors](t, w); w.Code != 400 || len(got.Fields) != 1 || got.Fields["history_dir"] != historyError {
			t.Errorf("%s: %d %+v", name, w.Code, got)
		}
		if saves := e.saves(); len(saves) != 0 {
			t.Errorf("%s: saved %+v", name, saves)
		}
	}

	if err := os.MkdirAll(filepath.Join(folder, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "history")
	if err := os.Symlink(filepath.Join(folder, "inside"), link); err != nil {
		t.Skipf("no symlinks: %v", err)
	}
	e := newAppEnv(t, nil)
	v := e.copyOnce(folder, link)
	if v.State != stCantWrite || !strings.Contains(v.Title, "inside the destination") {
		t.Errorf("a history folder that leads inside: %+v", v)
	}
	if saves := e.saves(); len(saves) != 1 || saves[0].HistoryDir != link {
		t.Errorf("saved %+v; want one save, of the link", saves)
	}
	if e.d.endpoints != nil {
		t.Errorf("dialed %q", e.d.endpoints)
	}
	if entries, err := os.ReadDir(filepath.Join(folder, "inside")); err != nil || len(entries) != 0 {
		t.Errorf("inside the folder: %v, %v", entries, err)
	}
}

// Clear history removes the chosen history folder's state file, not the
// default one's.
func TestClearHistoryDirChosen(t *testing.T) {
	folder, history := t.TempDir(), filepath.Join(t.TempDir(), "history")
	set := savedAt(folder)
	set.HistoryDir = history
	e := newAppEnv(t, set)
	for _, dir := range []string{history, e.s.stateDir} {
		st, err := state.Open(dir, folder, nil)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	chosen, _ := state.Path(history, folder)
	def, _ := state.Path(e.s.stateDir, folder)
	if w := e.post("/copy/api/clear", `{}`); w.Code != 200 {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(chosen); !os.IsNotExist(err) {
		t.Errorf("the chosen state file after clear: %v", err)
	}
	if _, err := os.Stat(def); err != nil {
		t.Errorf("the default state file after clear: %v", err)
	}
}

// A new history folder is a fresh history: Copy's preview and Free up
// space read it, not the old one, and a pass copies again what was moved
// out of the folder and records it there.
func TestCopyHistoryDirFresh(t *testing.T) {
	share := historyShare(t, "MyWorks/A/a.jpg", "MyWorks/B/b.jpg")
	folder := filepath.Join(t.TempDir(), "Seestar")
	r1, r2 := filepath.Join(t.TempDir(), "r1"), filepath.Join(t.TempDir(), "r2")
	e := newAppEnv(t, nil)
	e.d.dir = share
	if v := e.copyOnce(folder, r1); v.State != stDone {
		t.Fatalf("copy once: %+v", v)
	}
	moved := filepath.Join(folder, "MyWorks", "B", "b.jpg")
	if err := os.Rename(moved, filepath.Join(t.TempDir(), "b.jpg")); err != nil {
		t.Fatal(err)
	}

	// Browse shows b.jpg's status from the history folder saved.
	browseB := func() string {
		t.Helper()
		w := e.serve("GET", "/browse/api/list?path=MyWorks%2FB", "")
		if w.Code != 200 {
			t.Fatalf("list = %d %s", w.Code, w.Body)
		}
		return w.Body.String()
	}
	if got := browseB(); !strings.Contains(got, `"status":"copied"`) {
		t.Errorf("Browse with R1: %s", got)
	}

	// The records alone: R1 offers the moved file, R2 has no record of it.
	if got := e.freeFind(true); !slices.Contains(got, "MyWorks/B") {
		t.Errorf("trust preview with R1 = %q; want MyWorks/B", got)
	}
	if got := e.copyPreview(); slices.Contains(got, "MyWorks/B") {
		t.Errorf("copy preview with R1 = %q; want no MyWorks/B", got)
	}
	if w := e.post("/copy/api/settings", historySave(folder, r2)); w.Code != 200 {
		t.Fatalf("save R2: %d %s", w.Code, w.Body)
	}
	if v := e.s.free.view(); v.State != frStart || !v.Changed {
		t.Errorf("Free up space after a new history folder: %+v", v)
	}
	if v := e.s.preview.view(); v != nil {
		t.Errorf("Copy's preview after a new history folder: %+v", v)
	}
	if got := browseB(); !strings.Contains(got, `"status":"new"`) {
		t.Errorf("Browse with R2: %s", got)
	}
	if got := e.freeFind(true); slices.Contains(got, "MyWorks/B") {
		t.Errorf("trust preview with R2 = %q; want no MyWorks/B", got)
	}
	if got := e.freeFind(false); slices.Contains(got, "MyWorks/B") {
		t.Errorf("hash preview with R2 = %q; want no MyWorks/B", got)
	}
	if got := e.copyPreview(); !slices.Contains(got, "MyWorks/B") {
		t.Errorf("copy preview with R2 = %q; want MyWorks/B", got)
	}

	// A pass copies it again and records it in R2.
	if w := e.post("/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
		t.Fatalf("start copying: %d %s", w.Code, w.Body)
	}
	if v := e.waitCopy(stDone, stCantWrite); v.State != stDone {
		t.Fatalf("copy with R2: %+v", v)
	}
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("the moved file wasn't copied again: %v", err)
	}
	if records, err := state.Load(r2, folder, nil); err != nil || records["MyWorks/B/b.jpg"].Size == 0 {
		t.Errorf("R2's records = %v, %v", records, err)
	}
	if got := e.freeFind(false); !slices.Contains(got, "MyWorks/B") {
		t.Errorf("hash preview after the pass = %q; want MyWorks/B", got)
	}
}
