package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// eofSession holds a read once the underlying file has answered io.EOF,
// before the save consumes that answer: closing the session can't take
// back what was already read from it.
type eofSession struct {
	remote.Session
	entered, release chan struct{}
}

func (s *eofSession) Open(ctx context.Context, p string) (remote.File, error) {
	f, err := s.Session.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	return &eofFile{File: f, s: s}, nil
}

type eofFile struct {
	remote.File
	s *eofSession
}

func (f *eofFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	if err == io.EOF {
		close(f.s.entered)
		<-f.s.release
	}
	return n, err
}

// A save of one file whose bytes are all read when its folder becomes
// another telescope's never lands there. Either policy is safe: refusing
// the folder's reassignment until the save ends, or refusing the save's
// rename and leaving the file there as it was. This app takes the second.
func TestSaveCommitAfterReassignment(t *testing.T) {
	a, b, out := t.TempDir(), t.TempDir(), t.TempDir()
	e := newAppEnv(t, claimedAt(a, map[string]string{keyA: a, keyB: b}))
	e.key = keyA
	dst := filepath.Join(out, "saved.txt")
	if err := os.WriteFile(dst, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	sess := &eofSession{Session: remote.NewFake(fstest.MapFS{"at 192.0.2.10_445/x.txt": {Data: []byte("from A")}}), entered: make(chan struct{}), release: make(chan struct{})}
	e.s.dial = func(context.Context, Target) (remote.Session, error) { return sess, nil }
	var mu sync.Mutex
	var alerts []string
	e.s.native = Native{
		SaveFile: func(string) (string, error) { return dst, nil },
		Alert: func(m string) {
			mu.Lock()
			defer mu.Unlock()
			alerts = append(alerts, m)
		},
	}
	done := make(chan struct{})
	go func() { defer close(done); e.serve("GET", xURL, "") }()
	<-sess.entered
	// A's transfer has read every byte but not renamed yet. Make B the
	// target, then give B the save's folder.
	if err := e.m.Change(func() bool { return true }, func() error { e.set(func() { e.key = keyB; e.endpoint = atB }); return nil }); err != nil {
		t.Fatal(err)
	}
	w := e.post("/copy/api/settings", saveInput(out))
	if w.Code != 200 && w.Code != 400 && w.Code != 409 {
		t.Fatalf("reassignment: %d %s", w.Code, w.Body.String())
	}
	close(sess.release)
	<-done
	if w.Code != 200 {
		e.s.mu.Lock()
		folder := e.s.file.settings.FolderFor(keyB)
		e.s.mu.Unlock()
		if folder == out {
			t.Error("a refused reassignment still changed B's folder")
		}
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if w.Code == 200 && string(got) != "original" {
		t.Errorf("A saved %q into B's newly given folder after its session was closed", got)
	}
	// This app's policy: the reassignment stands, and the save says why it
	// saved nothing, leaving no temporary file.
	if w.Code != 200 {
		t.Errorf("reassignment = %d %s, want it to stand", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 1 || alerts[0] != "That folder became another telescope's while the file was saved. Save the file again outside it." {
		t.Errorf("alerts %q", alerts)
	}
	if names, _ := os.ReadDir(out); len(names) != 1 {
		t.Errorf("the folder holds %v, want just the original file", names)
	}
}

// A save's telescope is the one its session was dialed at, not whichever
// the target is by the time it saves: a session of another telescope's,
// still in the pool, saves nothing as this telescope's.
func TestSaveFromTheSessionsTelescope(t *testing.T) {
	a, b, out := t.TempDir(), t.TempDir(), t.TempDir()
	e := newAppEnv(t, claimedAt(a, map[string]string{keyA: a, keyB: b}))
	f := &fakeNative{}
	e.s.native = f.native()
	e.key = keyB
	// Viewing the file dials B's session, which the pool keeps.
	if w := e.serve("GET", strings.TrimSuffix(xURL, "&download=1"), ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	// The target is A now, without a change of route to replace the pool.
	e.set(func() { e.key = keyA })
	f.answer = filepath.Join(out, "saved.txt")
	e.serve("GET", xURL, "")
	if _, err := os.Stat(f.answer); err == nil {
		t.Error("B's file was saved as A's")
	}
	if len(f.alerts) != 1 || f.alerts[0] != "The telescope or its folder changed while you chose where to save. Save the file again." {
		t.Errorf("alerts %q", f.alerts)
	}
}

// A save into its telescope's folder renames through the root it opened
// on that folder, which keeps the folder when its path changes. So the
// commit checks the folder the root is open on: a folder moved to another
// path and given to another telescope, or reached through a link that
// now leads elsewhere while another telescope is given the folder, keeps
// the file that was there.
func TestSaveCommitWhereTheRootIs(t *testing.T) {
	const became = "That folder became another telescope's while the file was saved. Save the file again outside it."
	for _, tc := range []struct {
		name   string
		linked bool // whether A's folder is a link to it
		// move changes the paths once the save has read every byte, and
		// returns the folder to give B, which holds the existing file.
		move  func(t *testing.T, a, link string) string
		alert string
	}{
		{
			name: "renamed folder",
			move: func(t *testing.T, a, _ string) string {
				moved := filepath.Join(t.TempDir(), "moved")
				if err := os.Rename(a, moved); err != nil {
					t.Fatal(err)
				}
				return moved
			},
			alert: movedAlert,
		},
		{
			name:   "retargeted link",
			linked: true,
			move: func(t *testing.T, a, link string) string {
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), link); err != nil {
					t.Fatal(err)
				}
				return a
			},
			alert: became,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := t.TempDir(), t.TempDir()
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(a, link); err != nil {
				t.Fatal(err)
			}
			folder := a
			if tc.linked {
				folder = link
			}
			e := newAppEnv(t, claimedAt(folder, map[string]string{keyA: folder, keyB: b}))
			e.key = keyA
			if err := os.WriteFile(filepath.Join(a, "saved.txt"), []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			sess := &eofSession{Session: remote.NewFake(fstest.MapFS{"at 192.0.2.10_445/x.txt": {Data: []byte("from A")}}), entered: make(chan struct{}), release: make(chan struct{})}
			e.s.dial = func(context.Context, Target) (remote.Session, error) { return sess, nil }
			var mu sync.Mutex
			var alerts []string
			e.s.native = Native{
				SaveFile: func(string) (string, error) { return filepath.Join(folder, "saved.txt"), nil },
				Alert: func(m string) {
					mu.Lock()
					defer mu.Unlock()
					alerts = append(alerts, m)
				},
			}
			done := make(chan struct{})
			go func() { defer close(done); e.serve("GET", xURL, "") }()
			<-sess.entered
			// A's transfer has read every byte, with the folder's root and
			// lock open, but not renamed yet. Make B the target, move the
			// paths, then give B the folder.
			if err := e.m.Change(func() bool { return true }, func() error { e.set(func() { e.key = keyB; e.endpoint = atB }); return nil }); err != nil {
				t.Fatal(err)
			}
			given := tc.move(t, a, link)
			if w := e.post("/copy/api/settings", saveInput(given)); w.Code != 200 {
				t.Fatalf("giving B the folder: %d %s", w.Code, w.Body.String())
			}
			close(sess.release)
			<-done
			if got, err := os.ReadFile(filepath.Join(given, "saved.txt")); err != nil || string(got) != "original" {
				t.Errorf("B's file = %q, %v; want it as it was", got, err)
			}
			names, err := os.ReadDir(given)
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range names {
				if strings.HasSuffix(n.Name(), ".tmp") {
					t.Errorf("temporary file %s left", n.Name())
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(alerts) != 1 || alerts[0] != tc.alert {
				t.Errorf("alerts %q, want %q", alerts, tc.alert)
			}
		})
	}
}
