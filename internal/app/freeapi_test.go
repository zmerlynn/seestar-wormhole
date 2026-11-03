package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// The delete API checks its input and its preview, and Find saves the
// options.
func TestFreeAPI(t *testing.T) {
	e := newAppEnv(t, "")
	if w := e.post("/app/api/delete", `{"action":"find","days":"7"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "Couldn't find files: finish Setup first.") {
		t.Errorf("before Setup: %d %s", w.Code, w.Body)
	}
	e = newAppEnv(t, `{"version":1,"address":"192.0.2.10:445","folder":`+quote(abs("/data/Seestar"))+`}`)
	for _, days := range []string{"", "x", "-1", "1.5", "36501"} {
		w := e.post("/app/api/delete", `{"action":"find","days":`+quote(days)+`}`)
		got := decode[fieldErrors](t, w)
		if w.Code != 400 || got.Fields["days"] != "Enter a whole number of days, 0 or more." {
			t.Errorf("days %q: %d %+v", days, w.Code, got)
		}
	}
	if w := e.post("/app/api/delete", `{"action":"what"}`); w.Code != 400 {
		t.Errorf("unknown action: %d", w.Code)
	}
	w := e.post("/app/api/delete", `{"action":"delete","id":1,"folders":["MyWorks"]}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"Find files again before deleting."`) {
		t.Errorf("delete without a preview: %d %s", w.Code, w.Body)
	}

	// Find starts, however the check then ends, and saves the options.
	w = e.post("/app/api/delete", `{"action":"find","days":" 0 ","trust":true}`)
	if v := decode[freeView](t, w); w.Code != 200 {
		t.Errorf("find: %d %+v", w.Code, v)
	}
	f, err := loadSettings(e.path)
	if err != nil {
		t.Fatal(err)
	}
	if f.settings.OlderThanDays != 0 || !f.settings.TrustState {
		t.Errorf("saved options: %+v", f.settings)
	}
	if w := e.post("/app/api/settings", goodInput); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if f, _ := loadSettings(e.path); f.settings.OlderThanDays != 0 || !f.settings.TrustState {
		t.Errorf("a settings save changed the options: %+v", f.settings)
	}
}

// A preview, a settings save, a copy pass and a confirmed delete, all
// through the API, in one process: the delete removes the copied files.
func TestFreeInProcess(t *testing.T) {
	share := t.TempDir()
	folder := filepath.Join(t.TempDir(), "Seestar")
	e := newAppEnv(t, `{"version":1,"address":"192.0.2.10:445","folder":`+quote(folder)+`,"start_copying":false}`)
	for _, p := range []string{"MyWorks/M 42/a.jpg", "MyWorks/M 31/b.jpg"} {
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
	e.s.dial = func(ctx context.Context, endpoint string) (remote.Session, error) { return remote.NewFakeDir(share) }
	e.s.free.remover = func(ctx context.Context, endpoint string) (remote.Remover, error) { return remote.NewFakeDir(share) }
	free := func(body string) freeView {
		t.Helper()
		w := e.post("/app/api/delete", body)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
		return decode[freeView](t, w)
	}
	wait := func(states ...string) freeView {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			v := decode[struct {
				Free freeView `json:"free"`
			}](t, e.serve("GET", "/app/api/status", "", tokenHeader, testToken)).Free
			for _, s := range states {
				if v.State == s {
					return v
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("state %q, want %q: %+v", v.State, states, v)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Nothing is copied yet. The folder is there once copying has run,
	// so the first check proves by the records alone.
	free(`{"action":"find","days":"0","trust":true}`)
	if v := wait(frNothing, frPreview, frCheckFail); v.State != frNothing || v.Kept != "Kept: 2 not copied yet" {
		t.Errorf("before copying: %+v", v)
	}
	save := `{"address":"192.0.2.10:445","folder":` + quote(folder) + `,"interval":"10","excludes":[],"includes":[],"skip_built_in":true}`
	if w := e.post("/app/api/settings", save); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if v := wait(frNothing, frStart); v.State != frNothing {
		t.Errorf("a save that changed only the interval cleared the preview: %+v", v)
	}
	if w := e.post("/app/api/copy", `{"action":"start"}`); w.Code != 200 {
		t.Fatalf("start copying: %d %s", w.Code, w.Body)
	}
	for deadline := time.Now().Add(10 * time.Second); e.s.copy.view(time.Now()).State != stUpToDate; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("copying: %+v", e.s.copy.view(time.Now()))
		}
	}

	// A preview while copying is on, with the copies checked.
	free(`{"action":"find","days":"0","trust":false}`)
	v := wait(frPreview, frNothing, frCheckFail)
	if v.State != frPreview || v.Title != "2 files (6 B) are copied and safe to delete." || len(v.Groups) != 2 {
		t.Fatalf("preview: %+v", v)
	}
	free(fmt.Sprintf(`{"action":"delete","id":%d,"folders":["MyWorks/M 31","MyWorks/M 42"]}`, v.ID))
	if v := wait(frDone, frStopped, frFailed); v.Title != "Deleted 2 files and removed 3 empty folders." {
		t.Errorf("done: %+v", v)
	}
	for _, p := range []string{"MyWorks/M 42/a.jpg", "MyWorks/M 31/b.jpg"} {
		if _, err := os.Stat(filepath.Join(share, filepath.FromSlash(p))); !os.IsNotExist(err) {
			t.Errorf("%s: %v", p, err)
		}
		if _, err := os.Stat(filepath.Join(folder, filepath.FromSlash(p))); err != nil {
			t.Errorf("the copy of %s: %v", p, err)
		}
	}
	if v := free(`{"action":"reset"}`); v.State != frStart {
		t.Errorf("after Done: %+v", v)
	}
}

// A save that changes what a preview depends on clears it.
func TestFreeSaveClears(t *testing.T) {
	e := newAppEnv(t, `{"version":1,"address":"192.0.2.10:445","folder":`+quote(abs("/data/Seestar"))+`,"interval_minutes":5,"excludes":["Video","/Preview"]}`)
	e.s.free.st = freeStatus{state: frNothing, preview: &engine.DeleteResult{}}
	same := `{"address":"192.0.2.10:445","folder":` + quote(abs("/data/Seestar")) + `,"interval":"7","excludes":["Video","/Preview"],"includes":[],"skip_built_in":true}`
	for _, c := range []struct {
		body  string
		clear bool
	}{
		{same, false},
		{strings.Replace(same, `"includes":[]`, `"includes":["MyWorks"]`, 1), true},
		{strings.Replace(same, `"Video",`, ``, 1), true},
		{strings.Replace(same, `"skip_built_in":true`, `"skip_built_in":false`, 1), true},
		{strings.Replace(same, `192.0.2.10:445`, `192.0.2.11:445`, 1), true},
	} {
		if w := e.post("/app/api/settings", c.body); w.Code != 200 {
			t.Fatalf("save: %d %s", w.Code, w.Body)
		}
		v := e.s.free.view()
		if (v.State == frStart) != c.clear || v.Changed != c.clear {
			t.Errorf("%s: %+v", c.body, v)
		}
		e.s.free.st = freeStatus{state: frNothing, preview: &engine.DeleteResult{}}
		if w := e.post("/app/api/settings", same); w.Code != 200 {
			t.Fatal(w.Body)
		}
		e.s.free.st = freeStatus{state: frNothing, preview: &engine.DeleteResult{}}
	}
}
