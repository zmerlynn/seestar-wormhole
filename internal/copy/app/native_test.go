package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeNative records the dialogs shown and answers them as set.
type fakeNative struct {
	folders []string // the start folder of each picker shown
	saves   []string // the name of each save dialog shown
	alerts  []string
	answer  string // what each dialog returns
	err     error
}

func (f *fakeNative) native() *Native {
	return &Native{
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

func newNativeEnv(t *testing.T) (*appEnv, *fakeNative) {
	e := newAppEnv(t, `{"version":1,"address":"192.0.2.10:445","folder":`+quote(t.TempDir())+`}`)
	f := &fakeNative{}
	e.s.native = f.native()
	return e, f
}

func TestStatusNative(t *testing.T) {
	type status struct {
		Native bool `json:"native"`
	}
	e, _ := newNativeEnv(t)
	if !decode[status](t, e.serve("GET", "/app/api/status", "", tokenHeader, testToken)).Native {
		t.Error("a window's status says it has no dialogs")
	}
	if decode[status](t, newAppEnv(t, "").serve("GET", "/app/api/status", "", tokenHeader, testToken)).Native {
		t.Error("the browser-only status says it has dialogs")
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
		w := e.post("/app/api/folder", `{"folder":`+quote(tt.typed)+`}`)
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
	}](t, e.post("/app/api/folder", `{"folder":""}`)); got.Folder != "" {
		t.Errorf("Cancel = %q", got.Folder)
	}
	// The picker takes the same checks as every POST.
	if w := e.serve("POST", "/app/api/folder", `{}`, tokenHeader, testToken, "Origin", "http://evil.example", "Content-Type", "application/json"); w.Code != 403 {
		t.Errorf("a cross-origin picker = %d", w.Code)
	}
	if w := e.serve("POST", "/app/api/folder", `{}`, "Origin", "http://"+testAddr.String(), "Content-Type", "application/json"); w.Code != 403 {
		t.Errorf("a picker without the token = %d", w.Code)
	}
	if len(f.folders) != 2 {
		t.Errorf("%d pickers shown since the last start folder, want 2: Cancel's and none for the refused", len(f.folders))
	}
}

// TestChooseFolderFails checks that a picker that can't open says so, and
// how to go on.
func TestChooseFolderFails(t *testing.T) {
	e, f := newNativeEnv(t)
	f.err = errors.New("no display")
	w := e.post("/app/api/folder", `{"folder":""}`)
	got := decode[struct {
		Error string `json:"error"`
	}](t, w)
	if want := "Couldn't open the folder picker: no display. Type the folder's full path instead."; w.Code != 500 || got.Error != want {
		t.Errorf("got %d %q, want 500 %q", w.Code, got.Error, want)
	}
}

const xURL = "/file?path=at%20192.0.2.10_445%2Fx.txt&download=1"

// TestSaveFile checks that a download is saved where the save dialog says,
// and that Cancel writes nothing.
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
	// A file viewed, not downloaded, is browse's to serve.
	if w := e.serve("GET", strings.TrimSuffix(xURL, "&download=1"), ""); w.Code != 200 || w.Body.String() != "x" || len(f.saves) != 2 {
		t.Errorf("view = %d %q, %d dialogs", w.Code, w.Body, len(f.saves))
	}
}

// TestSaveFileFails checks that a failed save shows its reason without the
// temporary file's name.
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

// TestSaveFileRefused checks that a download browse refuses never reaches
// the save dialog.
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
		{"GET", "/file?path=..%2Fx.txt&download=1", nil, 400},
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

// TestRunQuit checks the window's side of Run: quit stops the server, and
// the window's context ends only after it has; ctx ending does the same.
func TestRunQuit(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, by := range []string{"quit", "ctx"} {
		t.Run(by, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var base string
			err := Run(ctx, Options{SettingsPath: filepath.Join(t.TempDir(), "app.json"), StateDir: t.TempDir(), Version: "v", Log: log},
				func(window context.Context, u string, quit func()) error {
					base, _, _ = strings.Cut(u, "/app/#")
					if resp, err := http.Get(base + "/app/"); err != nil {
						return err
					} else {
						resp.Body.Close()
					}
					if by == "quit" {
						quit()
					} else {
						cancel()
					}
					select {
					case <-window.Done():
					case <-time.After(5 * time.Second):
						t.Fatal("the window's context did not end")
					}
					if _, err := http.Get(base + "/app/"); err == nil {
						t.Error("the window's context ended while the app still serves")
					}
					return nil
				})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
