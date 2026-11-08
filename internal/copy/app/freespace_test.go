package app

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/engine"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// ancient is a modification time older than any days option tested.
var ancient = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// freeEnv is a freer over a share in a temp folder, and a copy of it.
type freeEnv struct {
	t        *testing.T
	f        *freer
	dir      string // the share
	stateDir string
	set      Settings
	mu       sync.Mutex
	faults   map[string]remote.Fault
	hold     chan struct{} // if not nil, a preview's dial waits for it
	refuse   bool          // the remover refuses every file
}

func newFreeEnv(t *testing.T, files ...string) *freeEnv {
	t.Helper()
	base := t.TempDir()
	e := &freeEnv{t: t, dir: filepath.Join(base, "share"), stateDir: filepath.Join(base, "state")}
	for _, p := range files {
		e.put(p, ancient)
	}
	e.set = DefaultSettings()
	e.set.Address, e.set.Folder, e.set.OlderThanDays = "192.0.2.10:445", filepath.Join(base, "Seestar"), 0
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.f = newFreer(t.Context(), e.stateDir, log, func(ctx context.Context, _ Target) (remote.Session, error) {
		e.mu.Lock()
		hold := e.hold
		e.mu.Unlock()
		if hold != nil {
			select {
			case <-hold:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return e.share()
	})
	e.f.remover = func(ctx context.Context, _ Target) (remote.Remover, error) {
		d, err := e.share()
		if e.refuse {
			return refuser{d}, err
		}
		return d, err
	}
	t.Cleanup(e.f.quit)
	return e
}

// refuser is a share that refuses to remove any file.
type refuser struct{ *remote.FakeDir }

func (r refuser) RemoveFile(ctx context.Context, en remote.Entry) error {
	return &fs.PathError{Op: "remove", Path: en.Path, Err: fs.ErrPermission}
}

func (e *freeEnv) share() (*remote.FakeDir, error) {
	d, err := remote.NewFakeDir(e.dir)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	d.Faults = e.faults
	e.mu.Unlock()
	return d, nil
}

// put writes the share's file p, modified at mtime.
func (e *freeEnv) put(p string, mtime time.Time) {
	e.t.Helper()
	name := filepath.Join(e.dir, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("abc"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chtimes(name, mtime, mtime); err != nil {
		e.t.Fatal(err)
	}
}

// has reports whether the share has p.
func (e *freeEnv) has(p string) bool {
	_, err := os.Lstat(filepath.Join(e.dir, filepath.FromSlash(p)))
	return err == nil
}

// copy runs one copy pass.
func (e *freeEnv) copy() {
	e.t.Helper()
	sel, err := selector(e.set)
	if err != nil {
		e.t.Fatal(err)
	}
	err = engine.RunSync(e.t.Context(), engine.SyncOptions{
		Endpoint: e.set.Address, Dest: e.set.Folder, StateDir: e.stateDir, Selector: sel, Once: true, Workers: 1,
		Dial: func(ctx context.Context, endpoint string) (remote.Session, error) { return e.share() },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		e.t.Fatal(err)
	}
}

// wait polls the view until its state is one of states.
func (e *freeEnv) wait(states ...string) freeView {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		v := e.f.view()
		if slices.Contains(states, v.State) {
			return v
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("state %q, want one of %q: %+v", v.State, states, v)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// preview finds files and waits for the preview.
func (e *freeEnv) preview() freeView {
	e.t.Helper()
	if err := e.f.find(e.set, Target{Endpoint: e.set.Address}); err != nil {
		e.t.Fatal(err)
	}
	return e.wait(frPreview, frNothing, frCheckFail)
}

func folders(v freeView) []string {
	var got []string
	for _, g := range v.Groups {
		got = append(got, g.Folder+" "+g.Name+" "+g.Size)
		for _, f := range g.Files {
			got = append(got, "  "+f.Path+" "+f.Size)
		}
	}
	return got
}

// A preview groups the files to delete by folder and says what it keeps;
// Delete removes only the files of the folders checked.
func TestFreePreviewAndDelete(t *testing.T) {
	e := newFreeEnv(t, "MyWorks/M 42/a.jpg", "MyWorks/M 42/b.jpg", "MyWorks/M 31/c.jpg", "MyWorks/M 31/d.jpg")
	e.copy()
	e.put("MyWorks/M 31/new.jpg", ancient)
	e.set.OlderThanDays = 7
	e.put("MyWorks/M 31/recent.jpg", time.Now().Add(-time.Hour))

	v := e.preview()
	if v.State != frPreview || v.Title != "4 files (12 B) are copied and safe to delete." ||
		v.Sub != "Only files older than 7 days · your copies checked" || v.ID == 0 {
		t.Errorf("preview: %+v", v)
	}
	want := []string{
		"MyWorks/M 31 MyWorks/M 31 6 B", "  MyWorks/M 31/c.jpg 3 B", "  MyWorks/M 31/d.jpg 3 B",
		"MyWorks/M 42 MyWorks/M 42 6 B", "  MyWorks/M 42/a.jpg 3 B", "  MyWorks/M 42/b.jpg 3 B",
	}
	if got := folders(v); !slices.Equal(got, want) {
		t.Errorf("groups\n%q\nwant\n%q", got, want)
	}
	if v.Kept != "Kept: 1 not copied yet, 1 newer than 7 days" && v.Kept != "Kept: 1 newer than 7 days, 1 not copied yet" {
		t.Errorf("kept %q", v.Kept)
	}
	wantKept := []failure{{"MyWorks/M 31/new.jpg", "not copied yet"}, {"MyWorks/M 31/recent.jpg", "newer than 7 days"}}
	if !slices.Equal(v.KeptList, wantKept) {
		t.Errorf("kept list %+v", v.KeptList)
	}
	// The check cleared the line once its last copy was checked.
	e.f.mu.Lock()
	current := e.f.st.current
	e.f.mu.Unlock()
	if current != "" {
		t.Errorf("after the check: current %q, want it empty", current)
	}

	if err := e.f.confirm(v.ID, []string{"MyWorks/M 42"}, Target{Endpoint: e.set.Address}); err != nil {
		t.Fatal(err)
	}
	d := e.wait(frDone, frStopped, frFailed)
	if d.State != frDone || d.Title != "Deleted 2 files and removed 1 empty folder." || d.Text != "Freed 6 B on the telescope." || d.Kept != "" {
		t.Errorf("done: %+v", d)
	}
	for p, want := range map[string]bool{
		"MyWorks/M 42/a.jpg": false, "MyWorks/M 42/b.jpg": false, "MyWorks/M 42": false,
		"MyWorks/M 31/c.jpg": true, "MyWorks/M 31/d.jpg": true, "MyWorks/M 31/new.jpg": true, "MyWorks/M 31/recent.jpg": true,
	} {
		if e.has(p) != want {
			t.Errorf("%s on the share: %v, want %v", p, !want, want)
		}
	}
	e.f.reset()
	if v := e.f.view(); v.State != frStart || v.Changed {
		t.Errorf("after reset: %+v", v)
	}
}

// watcher is a share that calls seen after each folder its walk decides on.
type watcher struct {
	*remote.FakeDir
	seen func()
}

func (w watcher) Walk(ctx context.Context, skip func(string, bool) bool, kept func(remote.Entry)) ([]remote.Entry, error) {
	return w.FakeDir.Walk(ctx, func(p string, dir bool) bool {
		s := skip(p, dir)
		if dir {
			w.seen()
		}
		return s
	}, kept)
}

// A delete first says it is checking the checked files again, and while it
// does, its status names no folder outside the ones checked.
func TestFreeRecheckChecked(t *testing.T) {
	e := newFreeEnv(t, "MyWorks/M 42/a.jpg", "MyWorks/M 42/b.jpg", "MyWorks/M 31/c.jpg", "Other/d.jpg", "Other/deep/e.jpg")
	e.copy()
	v := e.preview()
	var seen []string
	e.f.remover = func(ctx context.Context, _ Target) (remote.Remover, error) {
		d, err := e.share()
		return watcher{d, func() {
			v := e.f.view()
			seen = append(seen, v.Title+" | "+v.File)
		}}, err
	}
	if err := e.f.confirm(v.ID, []string{"MyWorks/M 42"}, Target{Endpoint: e.set.Address}); err != nil {
		t.Fatal(err)
	}
	if d := e.wait(frDone, frStopped, frFailed); d.Title != "Deleted 2 files and removed 1 empty folder." {
		t.Errorf("done: %+v", d)
	}
	// The walk reaches MyWorks, MyWorks/M 31, MyWorks/M 42 and Other, in
	// the share's order.
	if len(seen) != 4 || !slices.Contains(seen, "Checking 2 files again before deleting… | Listing MyWorks/M 42…") {
		t.Errorf("while checking again: %q", seen)
	}
	for _, s := range seen {
		if s != "Checking 2 files again before deleting… | " && s != "Checking 2 files again before deleting… | Listing MyWorks/M 42…" {
			t.Errorf("while checking again: %q", s)
		}
	}
}

// Delete is refused for a preview that is not the current one, and for
// no folder checked.
func TestFreeConfirmRefused(t *testing.T) {
	e := newFreeEnv(t, "MyWorks/a.jpg")
	e.copy()
	if err := e.f.confirm(1, []string{"MyWorks"}, Target{Endpoint: e.set.Address}); !errors.Is(err, errStale) {
		t.Errorf("before any preview: %v", err)
	}
	v := e.preview()
	if err := e.f.confirm(v.ID+1, []string{"MyWorks"}, Target{Endpoint: e.set.Address}); !errors.Is(err, errStale) {
		t.Errorf("another preview: %v", err)
	}
	if err := e.f.confirm(v.ID, nil, Target{Endpoint: e.set.Address}); err == nil || err.Error() != "no folder is checked" {
		t.Errorf("no folder: %v", err)
	}
	if err := e.f.find(e.set, Target{Endpoint: e.set.Address}); !errors.Is(err, errFreeBusy) && err != nil {
		t.Errorf("find again: %v", err)
	}
	e.wait(frPreview)
	if !e.has("MyWorks/a.jpg") {
		t.Error("a refused Delete removed a file")
	}
}

// A preview cleared or replaced after Delete's check but before its start
// is refused, and no remover is opened.
func TestFreeConfirmRace(t *testing.T) {
	for name, change := range map[string]func(e *freeEnv){
		"cleared": func(e *freeEnv) { e.f.clear() },
		"replaced": func(e *freeEnv) {
			if err := e.f.find(e.set, Target{Endpoint: e.set.Address}); err != nil {
				e.t.Fatal(err)
			}
			e.wait(frPreview)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newFreeEnv(t, "MyWorks/a.jpg")
			e.copy()
			v := e.preview()
			opened := 0
			e.f.remover = func(ctx context.Context, _ Target) (remote.Remover, error) {
				opened++
				return e.share()
			}
			e.f.prepared = func() { change(e) }
			if err := e.f.confirm(v.ID, []string{"MyWorks"}, Target{Endpoint: e.set.Address}); !errors.Is(err, errStale) {
				t.Errorf("confirm: %v, want errStale", err)
			}
			e.f.quit()
			if opened != 0 || !e.has("MyWorks/a.jpg") {
				t.Errorf("remover opened %d times; a.jpg on the share: %v", opened, e.has("MyWorks/a.jpg"))
			}
		})
	}
}

// A Save that changes what a preview depends on clears it, and ends a
// running check at Start; a cancelled check ends there without a notice.
func TestFreeStaleClearing(t *testing.T) {
	e := newFreeEnv(t, "MyWorks/a.jpg")
	e.copy()
	v := e.preview()
	e.f.clear()
	if c := e.f.view(); c.State != frStart || !c.Changed {
		t.Errorf("a cleared preview: %+v", c)
	}
	if err := e.f.confirm(v.ID, []string{"MyWorks"}, Target{Endpoint: e.set.Address}); !errors.Is(err, errStale) {
		t.Errorf("Delete of a cleared preview: %v", err)
	}

	e.mu.Lock()
	e.hold = make(chan struct{})
	e.mu.Unlock()
	if err := e.f.find(e.set, Target{Endpoint: e.set.Address}); err != nil {
		t.Fatal(err)
	}
	if c := e.f.view(); c.State != frChecking || c.Title != "Checking files…" || c.Changed {
		t.Errorf("checking: %+v", c)
	}
	e.f.clear()
	close(e.hold)
	if c := e.wait(frStart, frPreview); c.State != frStart || !c.Changed {
		t.Errorf("a check cleared while running: %+v", c)
	}

	e.mu.Lock()
	e.hold = make(chan struct{})
	e.mu.Unlock()
	if err := e.f.find(e.set, Target{Endpoint: e.set.Address}); err != nil {
		t.Fatal(err)
	}
	e.f.cancelCheck()
	if c := e.wait(frStart, frPreview); c.State != frStart || c.Changed {
		t.Errorf("a cancelled check: %+v", c)
	}
	if !e.has("MyWorks/a.jpg") {
		t.Error("a check removed a file")
	}
}

// Stop while a removal is held lets that file finish and deletes nothing
// after it.
func TestFreeStopHeld(t *testing.T) {
	files := []string{"MyWorks/a.jpg", "MyWorks/b.jpg", "MyWorks/c.jpg"}
	e := newFreeEnv(t, files...)
	e.copy()
	v := e.preview()
	held, release := make(chan string, len(files)), make(chan struct{})
	e.mu.Lock()
	e.faults = map[string]remote.Fault{}
	for _, p := range files {
		e.faults[p] = remote.Fault{BeforeRecheck: func() {
			held <- p
			<-release
		}}
	}
	e.mu.Unlock()
	if err := e.f.confirm(v.ID, []string{"MyWorks"}, Target{Endpoint: e.set.Address}); err != nil {
		t.Fatal(err)
	}
	var first string
	select {
	case first = <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("no removal was held")
	}
	// The held file's copy is checked, so no line names it yet.
	if d := e.f.view(); d.State != frDeleting || d.Title != "Deleting 1 of 3…" || d.Progress == nil || d.Progress.Total != 3 || d.Stopping ||
		d.File != "" {
		t.Errorf("deleting: %+v", d)
	}
	e.f.halt()
	e.f.halt()
	if d := e.f.view(); !d.Stopping {
		t.Errorf("stopping: %+v", d)
	}
	close(release)
	d := e.wait(frStopped, frDone, frFailed)
	if d.State != frStopped || d.Title != "Stopped after 1 file (3 B)." || d.Text != "Nothing else was deleted. Run it again to carry on." {
		t.Errorf("stopped: %+v", d)
	}
	for _, p := range files {
		if e.has(p) != (p != first) {
			t.Errorf("%s on the share: %v; the held file was %s", p, e.has(p), first)
		}
	}
}

// A refused removal ends the run and names the file.
func TestFreeFailed(t *testing.T) {
	e := newFreeEnv(t, "MyWorks/a.jpg")
	e.copy()
	v := e.preview()
	e.refuse = true
	if err := e.f.confirm(v.ID, []string{"MyWorks"}, Target{Endpoint: e.set.Address}); err != nil {
		t.Fatal(err)
	}
	d := e.wait(frFailed, frDone, frStopped)
	if d.State != frFailed || d.Title != "Stopped: the telescope refused to delete MyWorks/a.jpg (permission denied)." || d.Text != "No files were deleted." {
		t.Errorf("failed: %+v", d)
	}
}

// A check that can't reach the share says so.
func TestFreeCheckFail(t *testing.T) {
	e := newFreeEnv(t)
	if err := os.MkdirAll(e.set.Folder, 0o755); err != nil {
		t.Fatal(err)
	}
	e.f.dial = func(ctx context.Context, _ Target) (remote.Session, error) { return nil, errors.New("no route") }
	if err := e.f.find(e.set, Target{Endpoint: e.set.Address}); err != nil {
		t.Fatal(err)
	}
	v := e.wait(frCheckFail)
	if v.Title != "Couldn't finish checking files: connect to 192.0.2.10:445: no route." || v.Text != "Preview again to retry." {
		t.Errorf("check failed: %+v", v)
	}
	// A change of where the telescope is, or a Save, takes the error, which
	// was from before, back to Start: there was no preview to change.
	e.f.clear()
	if v := e.f.view(); v.State != frStart || v.Changed || v.Title != "" {
		t.Errorf("check failed, then cleared: %+v", v)
	}
}

// Free up space selects with its own patterns, after the built-in
// excludes, as `delete` does; Copy's patterns and its built-in switch play
// no part.
func TestFreePatterns(t *testing.T) {
	e := newFreeEnv(t, "MyWorks/M 42/a.jpg", "MyWorks/M 31/b.jpg", "MyWorks/M 31/c.mp4", "Other/d.jpg", "log/e.txt")
	e.set.SkipBuiltIn = false
	e.copy()
	paths := func(v freeView) []string {
		var got []string
		for _, g := range v.Groups {
			for _, f := range g.Files {
				got = append(got, f.Path)
			}
		}
		return got
	}
	// Copy's Leave out and Only copy no longer narrow a delete.
	e.set.Excludes, e.set.Includes = []string{"M 42"}, []string{"/Other"}
	for _, c := range []struct {
		name               string
		excludes, includes []string
		want               []string
	}{
		{"neither", nil, nil, []string{"MyWorks/M 31/b.jpg", "MyWorks/M 31/c.mp4", "MyWorks/M 42/a.jpg", "Other/d.jpg"}},
		{"keep", []string{"M 42", "*.mp4"}, nil, []string{"MyWorks/M 31/b.jpg", "Other/d.jpg"}},
		{"only", nil, []string{"/MyWorks"}, []string{"MyWorks/M 31/b.jpg", "MyWorks/M 31/c.mp4", "MyWorks/M 42/a.jpg"}},
		{"both", []string{"*.mp4"}, []string{"/MyWorks/M 31"}, []string{"MyWorks/M 31/b.jpg"}},
	} {
		e.set.FreeExcludes, e.set.FreeIncludes = c.excludes, c.includes
		if got := paths(e.preview()); !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	// The delete uses the preview's patterns.
	v := e.preview()
	if err := e.f.confirm(v.ID, []string{"MyWorks/M 31"}, Target{Endpoint: e.set.Address}); err != nil {
		t.Fatal(err)
	}
	if d := e.wait(frDone, frStopped, frFailed); d.Title != "Deleted 1 file." {
		t.Errorf("done: %+v", d)
	}
	for p, want := range map[string]bool{"MyWorks/M 31/b.jpg": false, "MyWorks/M 31/c.mp4": true, "log/e.txt": true} {
		if e.has(p) != want {
			t.Errorf("%s on the share: %v, want %v", p, !want, want)
		}
	}
}

// A ** pattern in Keep on the telescope keeps every file it matches, at any
// depth, and one in Only free up limits what is deleted to its matches.
func TestFreeDoubleStarPatterns(t *testing.T) {
	for _, c := range []struct {
		name               string
		excludes, includes []string
		deleted            []string
	}{
		{"keep", []string{"MyWorks/**/*.mp4", "**/M 42/"}, nil, []string{"MyWorks/M 31/x/b.jpg", "Other/d.jpg"}},
		{"only", nil, []string{"/MyWorks/**/*.JPG"}, []string{"MyWorks/M 31/x/b.jpg", "MyWorks/M 42/a.jpg"}},
	} {
		files := []string{"MyWorks/M 42/a.jpg", "MyWorks/M 31/x/b.jpg", "MyWorks/M 31/c.mp4", "Other/d.jpg"}
		e := newFreeEnv(t, files...)
		e.copy()
		e.set.FreeExcludes, e.set.FreeIncludes = c.excludes, c.includes
		v := e.preview()
		var listed, folders []string
		for _, g := range v.Groups {
			folders = append(folders, g.Folder)
			for _, f := range g.Files {
				listed = append(listed, f.Path)
			}
		}
		slices.Sort(listed)
		if !slices.Equal(listed, c.deleted) {
			t.Errorf("%s: preview lists %q, want %q", c.name, listed, c.deleted)
		}
		if err := e.f.confirm(v.ID, folders, Target{Endpoint: e.set.Address}); err != nil {
			t.Fatal(err)
		}
		if d := e.wait(frDone, frStopped, frFailed); d.State != frDone {
			t.Errorf("%s: done: %+v", c.name, d)
		}
		for _, p := range files {
			if e.has(p) == slices.Contains(c.deleted, p) {
				t.Errorf("%s: %s on the share: %v", c.name, p, e.has(p))
			}
		}
	}
}

// A saved Keep on the telescope or Only free up pattern that escapes a /
// is refused before the share is read: it never reaches a deletion, and
// the file it was meant to keep stays.
func TestFreeEscapedSlashRefused(t *testing.T) {
	e := newFreeEnv(t, "MyWorks/M31/a.fit")
	e.copy()
	for _, c := range []struct{ excludes, includes []string }{
		{[]string{`MyWorks\/M31`}, nil},
		{nil, []string{`MyWorks\/M31`}},
	} {
		e.set.FreeExcludes, e.set.FreeIncludes = c.excludes, c.includes
		err := e.f.find(e.set, Target{Endpoint: e.set.Address})
		if err == nil || !strings.Contains(err.Error(), "write a plain / between folders") {
			t.Errorf("%q %q: find: %v", c.excludes, c.includes, err)
		}
		if v := e.f.view(); v.State != frStart {
			t.Errorf("%q %q: state %q, want %q", c.excludes, c.includes, v.State, frStart)
		}
	}
	if !e.has("MyWorks/M31/a.fit") {
		t.Error("the file was deleted")
	}
}
