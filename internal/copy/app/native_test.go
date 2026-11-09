package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/browse"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
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

// A save into the images folder, under any spelling, takes the folder's
// lock, so it waits for no pass and lands in none: while another writer
// holds it the save is refused and writes nothing. A save elsewhere takes
// no lock, and the lock's own name in the folder is refused.
func TestSaveFileInImagesFolder(t *testing.T) {
	folder := t.TempDir()
	e := newAppEnv(t, savedAt(folder))
	f := &fakeNative{}
	e.s.native = f.native()
	sub := filepath.Join(folder, "MyWorks")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	save := func(to, wantAlert string) {
		t.Helper()
		f.answer, f.alerts = to, nil
		if w := e.serve("GET", xURL, ""); w.Code != http.StatusNoContent {
			t.Errorf("save to %s = %d", to, w.Code)
		}
		var want []string
		if wantAlert != "" {
			want = []string{wantAlert}
		}
		if !slices.Equal(f.alerts, want) {
			t.Errorf("save to %s: alerts %q, want %q", to, f.alerts, want)
		}
		b, err := os.ReadFile(to)
		if wantAlert == "" && string(b) != "x" {
			t.Errorf("save to %s: saved %q, %v", to, b, err)
		}
	}
	const busy = "Another copy or delete is using this folder. Try saving it again."

	root, err := os.OpenRoot(folder)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	held, err := state.LockFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	save(filepath.Join(sub, "busy.txt"), busy)
	if names, _ := os.ReadDir(sub); len(names) != 0 {
		t.Errorf("a refused save left %v", names)
	}
	if link := filepath.Join(t.TempDir(), "link"); os.Symlink(folder, link) == nil {
		save(filepath.Join(link, "MyWorks", "linked.txt"), busy)
	}
	save(filepath.Join(t.TempDir(), "outside.txt"), "")
	held.Close()

	save(filepath.Join(sub, "saved.txt"), "")
	again, err := state.LockFolder(root)
	if err != nil {
		t.Errorf("after a save the folder lock is %v, want it free", err)
	} else {
		again.Close()
	}
	save(filepath.Join(folder, ".Seestar-Wormhole.LOCK"), "That name is kept for the copy lock. Choose another name or folder.")
	if b, err := os.ReadFile(filepath.Join(folder, plan.LockName)); err != nil || len(b) != 0 {
		t.Errorf("the lock file: %q, %v, want it empty", b, err)
	}
}

// A save into the images folder holds the folder's lock for the whole
// download, not just while it starts: a second writer through its own
// root is refused while a read is held mid-file, and once the save fails
// the lock is free again.
func TestSaveFileHoldsFolderLockUntilDone(t *testing.T) {
	folder := t.TempDir()
	e := newAppEnv(t, savedAt(folder))
	entered := make(chan struct{})
	sessions := browse.NewSessions(t.Context(), func(context.Context) (remote.Session, error) {
		f := remote.NewFake(fstest.MapFS{"a.fit": {Data: []byte("abc")}})
		f.Faults = map[string]remote.Fault{"a.fit": {After: 1, Stall: func() { close(entered) }}}
		return f, nil
	}, slog.New(slog.DiscardHandler), browse.Reporter{})
	defer sessions.Close()
	lease, err := sessions.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	done := make(chan error, 1)
	go func() {
		_, err := e.s.saveTo(t.Context(), lease, "a.fit", filepath.Join(folder, "a.fit"))
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("the save ended before its read was held: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the save did not reach the held read")
	}
	root, err := os.OpenRoot(folder)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if held, err := state.LockFolder(root); !errors.Is(err, state.ErrFolderLocked) {
		if held != nil {
			held.Close()
		}
		t.Errorf("during the save a second writer got the folder lock: %v, want %v", err, state.ErrFolderLocked)
	}
	// Closing the sessions fails the held read, and with it the save.
	sessions.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the save succeeded after its session closed")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the save did not end after its session closed")
	}
	held, err := state.LockFolder(root)
	if err != nil {
		t.Errorf("after the failed save the folder lock is %v, want it free", err)
	} else {
		held.Close()
	}
}

// countedSession counts its Closes.
type countedSession struct {
	remote.Session
	closes atomic.Int32
}

func (s *countedSession) Close() error { s.closes.Add(1); return s.Session.Close() }

// A download keeps the opening of Browse it was asked in while its save
// dialog is open: opening Browse again meanwhile doesn't stop the save, or
// a Cancel, which saves nothing. Either way the old opening's session
// serves no other dial, and ends once the dialog's answer is done with.
func TestNativeDialogSurvivesBrowseReopen(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "save", true: "cancel"}[cancel], func(t *testing.T) {
			e, _ := newNativeEnv(t)
			var mu sync.Mutex
			var made []*countedSession
			e.s.dial = func(context.Context) (remote.Session, error) {
				mu.Lock()
				defer mu.Unlock()
				s := &countedSession{Session: remote.NewFake(fstest.MapFS{"at 192.0.2.10_445/x.txt": {Data: []byte("x")}})}
				made = append(made, s)
				return s, nil
			}
			dst := filepath.Join(t.TempDir(), "saved.txt")
			answer := dst
			if cancel {
				answer = ""
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var alerts []string
			e.s.native = Native{
				SaveFile: func(string) (string, error) { close(entered); <-release; return answer, nil },
				Alert:    func(s string) { alerts = append(alerts, s) },
			}
			if w := e.serve("GET", "/browse/", ""); w.Code != 200 {
				t.Fatal(w.Code)
			}
			done := make(chan int, 1)
			go func() { done <- e.serve("GET", xURL, "").Code }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("no save dialog")
			}
			if w := e.serve("GET", "/browse/", ""); w.Code != 200 {
				t.Fatal(w.Code)
			}
			close(release)
			select {
			case code := <-done:
				if code != http.StatusNoContent {
					t.Fatal(code)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the save didn't end")
			}
			b, err := os.ReadFile(dst)
			if cancel && !errors.Is(err, os.ErrNotExist) || !cancel && (err != nil || string(b) != "x") || len(alerts) != 0 {
				t.Fatalf("the reopening changed the save: data %q, err %v, alerts %q", b, err, alerts)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(made) != 1 {
				t.Fatalf("%d sessions, want the opening's one", len(made))
			}
			if n := made[0].closes.Load(); n != 1 {
				t.Fatalf("the opening's session closed %d times", n)
			}
		})
	}
}

// heldRead is a session whose first read waits for release.
type heldRead struct {
	countedSession
	entered, release chan struct{}
	once             sync.Once
}

func (s *heldRead) Open(ctx context.Context, p string) (remote.File, error) {
	f, err := s.Session.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	return &heldFile{File: f, s: s}, nil
}

type heldFile struct {
	remote.File
	s *heldRead
}

func (f *heldFile) Read(p []byte) (int, error) {
	f.s.once.Do(func() { close(f.s.entered); <-f.s.release })
	return f.File.Read(p)
}

// A download reading when Browse is opened again finishes on its own
// session, which then closes once; the new opening has its own pool.
func TestLeasedDownloadSurvivesBrowseReopen(t *testing.T) {
	e, _ := newNativeEnv(t)
	held := &heldRead{
		countedSession: countedSession{Session: remote.NewFake(fstest.MapFS{"a.fit": {Data: []byte("abc")}})},
		entered:        make(chan struct{}), release: make(chan struct{}),
	}
	e.s.dial = func(context.Context) (remote.Session, error) { return held, nil }
	if w := e.serve("GET", "/browse/", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	old := e.s.sessions
	dst := filepath.Join(t.TempDir(), "a.fit")
	e.s.native = Native{SaveFile: func(string) (string, error) { return dst, nil }}
	done := make(chan int, 1)
	go func() { done <- e.serve("GET", "/browse/file?path=a.fit&download=1", "").Code }()
	select {
	case <-held.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no read")
	}
	if w := e.serve("GET", "/browse/", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if e.s.sessions == old || held.closes.Load() != 0 {
		t.Fatalf("the reopening kept the pool or closed the reading session: %d closes", held.closes.Load())
	}
	close(held.release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the download didn't end")
	}
	if b, err := os.ReadFile(dst); err != nil || string(b) != "abc" || held.closes.Load() != 1 {
		t.Fatalf("data %q, err %v, %d closes", b, err, held.closes.Load())
	}
}
