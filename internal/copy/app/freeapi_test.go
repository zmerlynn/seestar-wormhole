package app

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/engine"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// The delete API checks its input and its preview, and Find saves the
// options.
func TestFreeAPI(t *testing.T) {
	e := newAppEnv(t, nil)
	if w := e.post("/copy/api/delete", `{"action":"find","days":"7"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "Couldn't preview: save the copy settings first.") {
		t.Errorf("before settings are saved: %d %s", w.Code, w.Body)
	}
	e = newAppEnv(t, savedAt(abs("/data/Seestar")))
	for _, days := range []string{"", "x", "-1", "1.5", "36501"} {
		w := e.post("/copy/api/delete", `{"action":"find","days":`+quote(days)+`}`)
		got := decode[fieldErrors](t, w)
		if w.Code != 400 || got.Fields["days"] != "Enter a whole number of days, 0 or more." {
			t.Errorf("days %q: %d %+v", days, w.Code, got)
		}
	}
	if w := e.post("/copy/api/delete", `{"action":"what"}`); w.Code != 400 {
		t.Errorf("unknown action: %d", w.Code)
	}
	w := e.post("/copy/api/delete", `{"action":"delete","id":1,"folders":["MyWorks"]}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"Preview again before deleting."`) {
		t.Errorf("delete without a preview: %d %s", w.Code, w.Body)
	}

	// Find starts, however the check then ends, and saves the options.
	w = e.post("/copy/api/delete", `{"action":"find","days":" 0 ","trust":true}`)
	if v := decode[freeView](t, w); w.Code != 200 {
		t.Errorf("find: %d %+v", w.Code, v)
	}
	last := func() Settings {
		saves := e.saves()
		if len(saves) == 0 {
			t.Fatal("nothing saved")
		}
		return saves[len(saves)-1]
	}
	if set := last(); set.OlderThanDays != 0 || !set.TrustState {
		t.Errorf("saved options: %+v", set)
	}
	if w := e.post("/copy/api/settings", goodInput); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if set := last(); set.OlderThanDays != 0 || !set.TrustState {
		t.Errorf("a settings save changed the options: %+v", set)
	}

	// Find checks and saves its own patterns, which a settings save keeps.
	idle := func() {
		for e.s.free.running() {
			time.Sleep(5 * time.Millisecond)
		}
	}
	idle()
	w = e.post("/copy/api/delete", `{"action":"find","days":"x","free_excludes":["ok","[x"],"free_includes":["","**/"]}`)
	got := decode[fieldErrors](t, w)
	want := map[string]string{
		"days":          "Enter a whole number of days, 0 or more.",
		"free_excludes": "Line 2: " + patternErr(t, "[x") + ". Correct this pattern and preview again.",
		"free_includes": "Line 2: " + patternErr(t, "**/") + ". Correct this pattern and preview again.",
	}
	if w.Code != 400 || !maps.Equal(got.Fields, want) {
		t.Errorf("bad patterns: %d %+v", w.Code, got)
	}
	w = e.post("/copy/api/delete", `{"action":"find","days":"0","trust":true,"free_excludes":["/MyWorks/M 31",""],"free_includes":["/MyWorks"]}`)
	if w.Code != 200 {
		t.Errorf("find: %d %s", w.Code, w.Body)
	}
	if set := last(); !slices.Equal(set.FreeExcludes, []string{"/MyWorks/M 31"}) || !slices.Equal(set.FreeIncludes, []string{"/MyWorks"}) {
		t.Errorf("saved patterns: %+v", set)
	}
	idle()
	if w := e.post("/copy/api/settings", goodInput); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if set := last(); !slices.Equal(set.FreeExcludes, []string{"/MyWorks/M 31"}) || !slices.Equal(set.FreeIncludes, []string{"/MyWorks"}) ||
		!slices.Equal(set.Excludes, []string{"Video", "/Preview"}) {
		t.Errorf("a settings save changed the patterns: %+v", set)
	}
}

// patternErr is the parser's message for a bad pattern.
func patternErr(t *testing.T, s string) string {
	t.Helper()
	_, err := engine.ParsePattern(s)
	if err == nil {
		t.Fatalf("%q parsed", s)
	}
	return err.Error()
}

// A preview, a settings save, a copy pass and a confirmed delete, all
// through the API, in one process: the delete removes the copied files.
func TestFreeInProcess(t *testing.T) {
	share := t.TempDir()
	folder := filepath.Join(t.TempDir(), "Seestar")
	e := newAppEnv(t, savedAt(folder))
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
	e.d.dir = share
	e.s.free.remover = func(ctx context.Context, endpoint string) (remote.Remover, error) { return remote.NewFakeDir(share) }
	free := func(body string) freeView {
		t.Helper()
		w := e.post("/copy/api/delete", body)
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
			}](t, e.serve("GET", "/copy/api/status", "")).Free
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
	save := `{"folder":` + quote(folder) + `,"interval":"10","files_at_once":"4","excludes":[],"includes":[],"skip_built_in":true}`
	if w := e.post("/copy/api/settings", save); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if v := wait(frNothing, frStart); v.State != frNothing {
		t.Errorf("a save that changed only the interval cleared the preview: %+v", v)
	}
	if w := e.post("/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
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
	// Activity has copy's records and Free up space's outcomes, each
	// tagged with its source and timed, newest first.
	lines, _ := e.s.copy.activity.lines()
	var deletes []string
	copies := 0
	for i, l := range lines {
		if l.At.IsZero() || i > 0 && l.At.After(lines[i-1].At) {
			t.Errorf("entry %d out of order or untimed: %+v", i, l)
		}
		switch l.Source {
		case "delete":
			deletes = append(deletes, l.Text)
		case "copy":
			copies++
		default:
			t.Errorf("entry %d has source %q", i, l.Source)
		}
	}
	want := []string{"free up space deleted files=2 size=\"6 B\"", "free up space can delete files=2 size=\"6 B\"", "free up space can delete files=0 size=\"0 B\""}
	if !slices.Equal(deletes, want) || copies == 0 {
		t.Errorf("delete activity %q, want %q; %d copy lines", deletes, want, copies)
	}
}

// A save that changes what a preview depends on clears it: the folder.
// Copy's patterns are not Free up space's.
func TestFreeSaveClears(t *testing.T) {
	set := savedAt(abs("/data/Seestar"))
	set.Excludes = []string{"Video", "/Preview"}
	e := newAppEnv(t, set)
	e.s.free.st = freeStatus{state: frNothing, preview: &engine.DeleteResult{}}
	same := `{"folder":` + quote(abs("/data/Seestar")) + `,"interval":"7","files_at_once":"4","excludes":["Video","/Preview"],"includes":[],"skip_built_in":true}`
	for _, c := range []struct {
		body  string
		clear bool
	}{
		{same, false},
		{strings.Replace(same, `"includes":[]`, `"includes":["MyWorks"]`, 1), false},
		{strings.Replace(same, `"Video",`, ``, 1), false},
		{strings.Replace(same, `"skip_built_in":true`, `"skip_built_in":false`, 1), false},
		{strings.Replace(same, quote(abs("/data/Seestar")), quote(abs("/data/Other")), 1), true},
	} {
		if w := e.post("/copy/api/settings", c.body); w.Code != 200 {
			t.Fatalf("save: %d %s", w.Code, w.Body)
		}
		v := e.s.free.view()
		if (v.State == frStart) != c.clear || v.Changed != c.clear {
			t.Errorf("%s: %+v", c.body, v)
		}
		e.s.free.st = freeStatus{state: frNothing, preview: &engine.DeleteResult{}}
		if w := e.post("/copy/api/settings", same); w.Code != 200 {
			t.Fatal(w.Body)
		}
		e.s.free.st = freeStatus{state: frNothing, preview: &engine.DeleteResult{}}
	}
}
