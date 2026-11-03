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
	"testing"
	"testing/fstest"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// previewEnv is a previewer of a fake share, with copyEnv's settings.
type previewEnv struct {
	*copyEnv
	p *previewer
}

func newPreviewEnv(t *testing.T, files fstest.MapFS) *previewEnv {
	t.Helper()
	e := &previewEnv{copyEnv: newCopyEnv(t, files)}
	e.p = newPreviewer(t.Context(), e.stateDir, slog.New(slog.NewTextHandler(io.Discard, nil)), e.c.dial)
	t.Cleanup(e.p.quit)
	return e
}

// run starts a preview and waits for it to end.
func (e *previewEnv) run() *previewView {
	e.t.Helper()
	if err := e.p.start(e.set); err != nil {
		e.t.Fatal(err)
	}
	e.p.mu.Lock()
	done := e.p.done
	e.p.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			e.t.Fatal("the preview did not end")
		}
	}
	return e.p.view()
}

// A preview shows what Start would do, by folder, and changes nothing: no
// folder, no state, no lock.
func TestPreview(t *testing.T) {
	files := fstest.MapFS{
		"MyWorks/M 31/done.jpg": image("ddd"),
		"MyWorks/M 31/re.jpg":   image("rr"),
	}
	e := newPreviewEnv(t, files)
	e.set.Excludes = []string{"Video"}
	o := engine.SyncOptions{
		Endpoint: e.set.Address, Dest: e.set.Folder, StateDir: e.stateDir, Once: true, Workers: 1,
		Dial: func(context.Context, string) (remote.Session, error) { return remote.NewFake(files), nil },
	}
	if err := engine.RunSync(t.Context(), o, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	files["MyWorks/M 31/re.jpg"] = image("rrrr")
	files["MyWorks/M 31/new.jpg"] = image("nnnnnn")
	files["MyWorks/M 42/a.jpg"] = image("aaaaaaaa")
	files["MyWorks/M 42/b.jpg"] = image("bb")
	files["MyWorks/have.jpg"] = image("hh")
	files["MyWorks/young.jpg"] = &fstest.MapFile{Data: []byte("y"), ModTime: time.Now()}
	files["MyWorks/x.jpg"] = image("x")
	files["MyWorks/X.jpg"] = image("X")
	files["Video/v.mp4"] = image("v")
	files["top.jpg"] = image("t")
	have := filepath.Join(e.set.Folder, "MyWorks", "have.jpg")
	writeFile(t, have, "hh")
	if err := os.Chtimes(have, oldMTime, oldMTime); err != nil {
		t.Fatal(err)
	}
	before := snapshotDir(t, e.set.Folder)
	beforeState := snapshotDir(t, e.stateDir)

	v := e.run()
	if v == nil || v.State != pvDone || v.Title != "Would copy 5 files (21 B); 2 can't be copied." {
		t.Fatalf("got %+v, want 5 files to copy and 2 that can't be", v)
	}
	if want := "Already copied or in your folder 2 · Still being written 1 · Left out 1"; v.Sub != want {
		t.Errorf("sub: got %q, want %q", v.Sub, want)
	}
	var got []string
	for _, g := range v.Groups {
		got = append(got, g.Name+" "+num(g.Count)+" "+g.Size)
		if g.Files != nil {
			t.Errorf("%s: got files %v, want none", g.Name, g.Files)
		}
	}
	if want := []string{"Share 1 1 B", "MyWorks/M 31 2 10 B", "MyWorks/M 42 2 10 B"}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("groups: got %q, want %q", got, want)
	}
	if after := snapshotDir(t, e.set.Folder); after != before {
		t.Errorf("the folder changed:\nbefore %s\nafter  %s", before, after)
	}
	if after := snapshotDir(t, e.stateDir); after != beforeState {
		t.Errorf("the state changed:\nbefore %s\nafter  %s", beforeState, after)
	}
	if e.locked() {
		t.Error("the folder is locked after a preview")
	}
}

// snapshotDir describes every entry under root: path, mode, size and, for
// files, mtime. Windows updates a folder's mtime late, after the file
// written into it, so a folder is described by its entries.
func snapshotDir(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		b.WriteString(p + " " + fi.Mode().String() + " " + num(int(fi.Size())))
		if !d.IsDir() {
			b.WriteString(" " + fi.ModTime().String())
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// A preview with nothing new says so; one of an empty share has no sub.
func TestPreviewNothing(t *testing.T) {
	e := newPreviewEnv(t, fstest.MapFS{})
	if v := e.run(); v.State != pvDone || v.Title != "Nothing to copy." || v.Sub != "" || v.Groups != nil {
		t.Errorf("got %+v, want Nothing to copy", v)
	}
}

// failDir is a session whose listing fails at dir.
type failDir struct {
	remote.Session
	dir string
}

func (f failDir) Walk(context.Context, func(string, bool) bool) ([]remote.Entry, error) {
	return nil, &fs.PathError{Op: "readdir", Path: f.dir, Err: errors.New("access denied")}
}

// A failed preview words its cause as Copy does: the connection, a
// listing, or anything else.
func TestPreviewFails(t *testing.T) {
	e := newPreviewEnv(t, fstest.MapFS{"MyWorks/M 42/a.jpg": image("a")})
	e.dialErr = errors.New("no route")
	v := e.run()
	if v.State != pvFailed || v.Title != "Can't reach the telescope at 192.0.2.10:445." || v.Text != checkReach+" Preview again to retry." {
		t.Errorf("unreachable: %+v", v)
	}
	e.dialErr = nil
	e.p.dial = func(ctx context.Context, endpoint string) (remote.Session, error) {
		s, err := e.c.dial(ctx, endpoint)
		return failDir{s, "MyWorks/M 42"}, err
	}
	if v := e.run(); v.State != pvFailed || v.Title != "Couldn't read MyWorks/M 42." || v.Text != "Preview again to retry." {
		t.Errorf("listing failed: %+v", v)
	}
	writeFile(t, e.set.Folder, "not a folder")
	if v := e.run(); v.State != pvFailed || !strings.HasPrefix(v.Title, "Couldn't preview: open destination: ") || v.Text != "Preview again to retry." {
		t.Errorf("folder is a file: %+v", v)
	}
}

// Discard ends a running preview, which then shows nothing; a second start
// while one runs is refused.
func TestPreviewDiscard(t *testing.T) {
	e := newPreviewEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("a")})
	dialed := make(chan struct{})
	e.p.dial = func(ctx context.Context, endpoint string) (remote.Session, error) {
		close(dialed)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := e.p.start(e.set); err != nil {
		t.Fatal(err)
	}
	<-dialed
	if v := e.p.view(); v == nil || v.State != pvRunning || v.Title != "Connecting to 192.0.2.10:445…" {
		t.Errorf("running: %+v", v)
	}
	if err := e.p.start(e.set); !errors.Is(err, errPreviewing) {
		t.Errorf("a second start: got %v, want errPreviewing", err)
	}
	e.p.discard()
	if v := e.p.view(); v != nil {
		t.Errorf("after discard: %+v, want none", v)
	}
	e.p.quit()
	if v := e.p.view(); v != nil {
		t.Errorf("after the run ended: %+v, want none", v)
	}
	if err := e.p.start(e.set); !errors.Is(err, errBusy) {
		t.Errorf("start after quit: got %v, want errBusy", err)
	}
}

// onClose is a session that calls f as the dry run closes it, after its
// decisions.
type onClose struct {
	remote.Session
	f func()
}

func (s onClose) Close() error {
	s.f()
	return s.Session.Close()
}

// A preview discarded as its run returns stays discarded: the run doesn't
// publish its result, nor its failure.
func TestPreviewDiscardAtEnd(t *testing.T) {
	for _, name := range []string{"done", "failed"} {
		t.Run(name, func(t *testing.T) {
			e := newPreviewEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("a")})
			e.p.dial = func(ctx context.Context, endpoint string) (remote.Session, error) {
				s, err := e.c.dial(ctx, endpoint)
				if name == "failed" {
					s = failDir{s, "MyWorks"}
				}
				// The preview is dropped, but its context is canceled
				// only after the run sees it wasn't.
				return onClose{s, func() {
					e.p.mu.Lock()
					e.p.st = previewStatus{}
					e.p.mu.Unlock()
				}}, err
			}
			if v := e.run(); v != nil {
				t.Errorf("got %+v, want none", v)
			}
		})
	}
}

// The listing's progress shows as Copy's does.
func TestPreviewListing(t *testing.T) {
	e := newPreviewEnv(t, nil)
	e.p.st = previewStatus{state: pvRunning, endpoint: "192.0.2.10:445", listing: true}
	if v := e.p.view(); v.Title != "Looking for new files…" {
		t.Errorf("listing: %+v", v)
	}
	e.p.st.folders = 12
	if v := e.p.view(); v.Title != "Looking for new files… 12 folders checked." || v.Path != "" {
		t.Errorf("listing: %+v", v)
	}
	// A real run names each folder as it is listed.
	e = newPreviewEnv(t, fstest.MapFS{"MyWorks/M 31/a.jpg": image("a")})
	var seen []string
	dial := e.p.dial
	e.p.dial = func(ctx context.Context, endpoint string) (remote.Session, error) {
		sess, err := dial(ctx, endpoint)
		return walkSpy{sess, func(p string, dir bool) {
			if v := e.p.view(); dir && v != nil {
				seen = append(seen, v.Title+" "+v.Path)
			}
		}}, err
	}
	e.run()
	want := []string{"Looking for new files… 1 folder checked. Listing MyWorks…", "Looking for new files… 2 folders checked. Listing MyWorks/M 31…"}
	if !slices.Equal(seen, want) {
		t.Errorf("while listing: got %q, want %q", seen, want)
	}
}

// walkSpy is a session that calls after with each entry of a walk, after
// the walk's own skip func.
type walkSpy struct {
	remote.Session
	after func(p string, dir bool)
}

func (s walkSpy) Walk(ctx context.Context, skip func(string, bool) bool) ([]remote.Entry, error) {
	return s.Session.Walk(ctx, func(p string, dir bool) bool {
		out := skip(p, dir)
		s.after(p, dir)
		return out
	})
}

// The copy API runs and cancels a preview while copying is off. Start,
// a Save that changes the selection and Clear history discard it.
func TestPreviewAPI(t *testing.T) {
	e := newAppEnv(t, "")
	if w := e.post("/app/api/copy", `{"action":"preview"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "Couldn't preview: set the telescope and folder in Settings first.") {
		t.Errorf("preview before settings are saved: %d %q", w.Code, w.Body.String())
	}
	input := `{"address":"192.0.2.10","folder":` + quote(filepath.Join(t.TempDir(), "Seestar")) + `,"interval":"5","skip_built_in":true}`
	if w := e.post("/app/api/settings", input); w.Code != 200 {
		t.Fatalf("save: %d %q", w.Code, w.Body.String())
	}
	preview := func() *previewView {
		t.Helper()
		w := e.post("/app/api/copy", `{"action":"preview"}`)
		if w.Code != 200 {
			t.Fatalf("preview: %d %q", w.Code, w.Body.String())
		}
		e.s.preview.mu.Lock()
		done := e.s.preview.done
		e.s.preview.mu.Unlock()
		if done != nil {
			<-done
		}
		v := decode[struct {
			Copy copyView `json:"copy"`
		}](t, e.serve("GET", "/app/api/status", "", tokenHeader, testToken)).Copy
		if v.State != stOff || v.Preview == nil {
			t.Fatalf("status after a preview: %+v", v)
		}
		return v.Preview
	}
	// The test share's one file has no mtime, so it can't be copied.
	if v := preview(); v.State != pvDone || v.Title != "Preview finished: 1 file can't be copied." || v.Sub != "" {
		t.Errorf("preview: %+v", v)
	}
	if w := e.post("/app/api/copy", `{"action":"cancel"}`); w.Code != 200 || decode[copyView](t, w).Preview != nil {
		t.Errorf("cancel: %d %q", w.Code, w.Body.String())
	}
	preview()
	if w := e.post("/app/api/settings", strings.Replace(input, `"interval":"5"`, `"interval":"7"`, 1)); w.Code != 200 || e.s.preview.view() == nil {
		t.Errorf("a save of the interval alone: %d, preview %+v; want it kept", w.Code, e.s.preview.view())
	}
	if w := e.post("/app/api/settings", strings.Replace(input, `"skip_built_in":true`, `"skip_built_in":false`, 1)); w.Code != 200 || e.s.preview.view() != nil {
		t.Errorf("a save of the selection: %d, preview %+v; want none", w.Code, e.s.preview.view())
	}
	preview()
	if w := e.post("/app/api/clear", `{}`); w.Code != 200 || e.s.preview.view() != nil {
		t.Errorf("clear history: %d, preview %+v; want none", w.Code, e.s.preview.view())
	}
	preview()
	w := e.post("/app/api/copy", `{"action":"start"}`)
	if v := decode[copyView](t, w); w.Code != 200 || !v.On || v.Preview != nil {
		t.Errorf("start: %d %+v, want on and no preview", w.Code, v)
	}
	if w := e.post("/app/api/copy", `{"action":"preview"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "Couldn't preview: copying is on.") {
		t.Errorf("preview while copying: %d %q", w.Code, w.Body.String())
	}
}
