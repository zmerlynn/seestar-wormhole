package app

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeNative records the dialogs shown and answers them as set.
type fakeNative struct {
	folders []string // the start folder of each picker shown
	saves   []string // the name of each save dialog shown
	alerts  []string
	answer  string // what each dialog returns
	err     error
}

func (f *fakeNative) native() Native {
	return Native{
		ChooseFolder: func(dir string) (string, error) {
			f.folders = append(f.folders, dir)
			return f.answer, f.err
		},
		SaveFile: func(name string) (string, error) {
			f.saves = append(f.saves, name)
			return f.answer, f.err
		},
		Alert: func(message string) {
			f.alerts = append(f.alerts, message)
		},
	}
}

// newNativeEnv is an app env with saved settings, so Browse is there, and
// a window's dialogs.
func newNativeEnv(t *testing.T) (*appEnv, *fakeNative) {
	e := newAppEnv(t, savedAt(t.TempDir()))
	f := &fakeNative{}
	e.s.native = f.native()
	return e, f
}

// The status says whether the page may offer Choose…: only with the
// window's folder picker.
func TestStatusChoose(t *testing.T) {
	type status struct {
		Choose bool `json:"choose"`
	}
	e, _ := newNativeEnv(t)
	if !decode[status](t, e.serve("GET", "/copy/api/status", "")).Choose {
		t.Error("a window's status has no folder picker")
	}
	if decode[status](t, newAppEnv(t, nil).serve("GET", "/copy/api/status", "")).Choose {
		t.Error("the browser's status has a folder picker")
	}
}

// Without a window there is no folder picker to ask for.
func TestChooseFolderBrowser(t *testing.T) {
	e := newAppEnv(t, nil)
	if w := e.post("/copy/api/folder", `{"folder":""}`); w.Code != http.StatusNotFound {
		t.Errorf("folder without a window = %d", w.Code)
	}
}

func TestChooseFolder(t *testing.T) {
	e, f := newNativeEnv(t)
	dir := t.TempDir()
	f.answer = filepath.Join(dir, "chosen")
	for _, tt := range []struct{ typed, start string }{
		{dir, dir},
		{filepath.Join(dir, "a", "b"), dir}, // the nearest folder that exists
		{"relative", ""},
		{"", ""},
	} {
		f.folders = nil
		w := e.post("/copy/api/folder", `{"folder":`+quote(tt.typed)+`}`)
		got := decode[struct {
			Folder string `json:"folder"`
		}](t, w)
		if w.Code != 200 || got.Folder != f.answer || len(f.folders) != 1 || f.folders[0] != tt.start {
			t.Errorf("%q: %d %+v, picker started at %q, want %q", tt.typed, w.Code, got, f.folders, tt.start)
		}
	}
	f.answer = "" // Cancel
	if got := decode[struct {
		Folder string `json:"folder"`
	}](t, e.post("/copy/api/folder", `{"folder":""}`)); got.Folder != "" {
		t.Errorf("Cancel = %q", got.Folder)
	}
	if w := e.serve("GET", "/copy/api/folder", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET folder = %d", w.Code)
	}
	if len(e.saves()) != 0 {
		t.Errorf("the picker saved settings: %+v", e.saves())
	}
}

// A picker that can't open says so, and how to go on.
func TestChooseFolderFails(t *testing.T) {
	e, f := newNativeEnv(t)
	f.err = errors.New("no display")
	w := e.post("/copy/api/folder", `{"folder":""}`)
	got := decode[struct {
		Error string `json:"error"`
	}](t, w)
	if want := "Couldn't open the folder picker: no display. Type the folder's full path instead."; w.Code != 500 || got.Error != want {
		t.Errorf("got %d %q, want 500 %q", w.Code, got.Error, want)
	}
}

const xURL = "/browse/file?path=at%20192.0.2.10_445%2Fx.txt&download=1"

// A download in a window is saved where the save dialog says, and Cancel
// writes nothing.
func TestSaveFile(t *testing.T) {
	e, f := newNativeEnv(t)
	dir := t.TempDir()
	f.answer = filepath.Join(dir, "saved.txt")
	w := e.serve("GET", xURL, "")
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 || len(f.saves) != 1 || f.saves[0] != "x.txt" || len(f.alerts) != 0 {
		t.Errorf("save = %d %q, dialogs %q, alerts %q", w.Code, w.Body, f.saves, f.alerts)
	}
	if b, _ := os.ReadFile(f.answer); string(b) != "x" {
		t.Errorf("saved %q", b)
	}
	f.answer = "" // Cancel
	if w := e.serve("GET", strings.Replace(xURL, "x.txt", "y.txt", 1), ""); w.Code != http.StatusNoContent || len(f.alerts) != 0 {
		t.Errorf("Cancel = %d, alerts %q", w.Code, f.alerts)
	}
	if names, _ := os.ReadDir(dir); len(names) != 1 {
		t.Errorf("Cancel wrote %v", names)
	}
	// A file viewed, not downloaded, is Browse's to serve.
	if w := e.serve("GET", strings.TrimSuffix(xURL, "&download=1"), ""); w.Code != 200 || w.Body.String() != "x" || len(f.saves) != 2 {
		t.Errorf("view = %d %q, %d dialogs", w.Code, w.Body, len(f.saves))
	}
}

// Without a window, a download is the browser's: Browse sends the file.
func TestSaveFileBrowser(t *testing.T) {
	e := newAppEnv(t, savedAt(t.TempDir()))
	w := e.serve("GET", xURL, "")
	if w.Code != 200 || w.Body.String() != "x" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("download = %d %q %q", w.Code, w.Body, w.Header().Get("Content-Disposition"))
	}
}

// A failed save shows its reason without the temporary file's name.
func TestSaveFileFails(t *testing.T) {
	e, f := newNativeEnv(t)
	f.answer = filepath.Join(t.TempDir(), "missing", "saved.txt")
	if w := e.serve("GET", xURL, ""); w.Code != http.StatusNoContent {
		t.Errorf("save = %d", w.Code)
	}
	if len(f.alerts) != 1 || !strings.HasPrefix(f.alerts[0], "Couldn't save x.txt: ") || !strings.HasSuffix(f.alerts[0], ". Try saving it again.") ||
		strings.Contains(f.alerts[0], ".tmp") {
		t.Errorf("alerts %q", f.alerts)
	}
	f.alerts = nil
	f.answer = filepath.Join(t.TempDir(), "gone.txt")
	e.serve("GET", strings.Replace(xURL, "x.txt", "gone.txt", 1), "")
	if len(f.alerts) != 1 || f.alerts[0] != "Couldn't save gone.txt: file does not exist. Try saving it again." {
		t.Errorf("alerts %q", f.alerts)
	}
}

// A download Browse refuses never reaches the save dialog.
func TestSaveFileRefused(t *testing.T) {
	e, f := newNativeEnv(t)
	f.answer = filepath.Join(t.TempDir(), "saved.txt")
	for _, tt := range []struct {
		method, target string
		headers        []string
		code           int
	}{
		{"GET", xURL, []string{"Host", "localhost:53818"}, 403},
		{"GET", xURL, []string{"Sec-Fetch-Site", "cross-site"}, 403},
		{"GET", xURL, []string{"Sec-Fetch-Site", "same-site"}, 403},
		{"POST", xURL, nil, 405},
		{"HEAD", xURL, nil, 200},
		{"GET", xURL + "&path=x", nil, 400},
		{"GET", "/browse/file?path=..%2Fx.txt&download=1", nil, 400},
	} {
		if w := e.serve(tt.method, tt.target, "", tt.headers...); w.Code != tt.code {
			t.Errorf("%s %s %q = %d, want %d", tt.method, tt.target, tt.headers, w.Code, tt.code)
		}
	}
	if len(f.saves) != 0 {
		t.Errorf("save dialogs shown for refused requests: %q", f.saves)
	}
	if _, err := os.Stat(f.answer); err == nil {
		t.Error("a refused request saved a file")
	}
}
