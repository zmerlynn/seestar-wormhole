package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// share is a fake device over a local folder that a delete run can
// remove from, with a destination and a state folder beside it.
type share struct {
	dir, dest, stateDir string
	faults              map[string]remote.Fault
	last                *remote.FakeDir
}

func newShare(t *testing.T) *share {
	base := t.TempDir()
	s := &share{dir: filepath.Join(base, "share"), dest: filepath.Join(base, "dest"), stateDir: filepath.Join(base, "state")}
	for _, d := range []string{s.dir, s.dest} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// put writes p with data and oldMTime under root.
func put(t *testing.T, root, p, data string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, oldMTime, oldMTime); err != nil {
		t.Fatal(err)
	}
}

// has reports whether p is on the share.
func (s *share) has(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(s.dir, filepath.FromSlash(p)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// record records p as copied with data, as listed with oldMTime.
func (s *share) record(t *testing.T, recs map[string]string) {
	t.Helper()
	st, err := state.Open(s.stateDir, s.dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for p, data := range recs {
		sum := sha256.Sum256([]byte(data))
		rec := state.Record{Path: p, Size: int64(len(data)), MTime: oldMTime, SHA256: hex.EncodeToString(sum[:]), At: oldMTime, How: state.Copied}
		if err := st.Append(rec); err != nil {
			t.Fatal(err)
		}
	}
}

// options returns the options of a real run on the share, proven by the
// copies, of any age.
func (s *share) options() DeleteOptions {
	open := func() (*remote.FakeDir, error) {
		f, err := remote.NewFakeDir(s.dir)
		if err != nil {
			return nil, err
		}
		f.Faults = s.faults
		s.last = f
		return f, nil
	}
	return DeleteOptions{
		Endpoint: "device:445", Dest: s.dest, StateDir: s.stateDir,
		Dial:        func(context.Context, string) (remote.Session, error) { return open() },
		OpenRemover: func(context.Context, string) (remote.Remover, error) { return open() },
	}
}

// outcomes renders a result's files as "path outcome", for comparison.
func outcomes(r DeleteResult) []string {
	names := []string{FileDeleted: "deleted", FileKept: "kept", FileRecent: "recent", FileFailed: "failed"}
	var out []string
	for _, f := range r.Files {
		out = append(out, f.Path+" "+names[f.Outcome])
	}
	return out
}

// A preview and a real run return each file's outcome and call the hooks;
// only the real run calls Deleted.
func TestDeleteResultAndHooks(t *testing.T) {
	s := newShare(t)
	files := map[string]string{"MyWorks/a.jpg": "aaa", "MyWorks/b.jpg": "bb"}
	for p, data := range files {
		put(t, s.dir, p, data)
		put(t, s.dest, p, data)
	}
	put(t, s.dir, "MyWorks/new.jpg", "n") // not in the state
	s.record(t, files)
	for _, dry := range []bool{true, false} {
		o := s.options()
		o.DryRun = dry
		var checked []int
		var hashed [][2]int64
		var listed, hashing, deleted []string
		o.Hooks = Hooks{
			Listed:  func(p string, n int) { listed = append(listed, fmt.Sprint(n, " ", p)) },
			Checked: func(n int) { checked = append(checked, n) },
			Hashing: func(p string) { hashing = append(hashing, p) },
			Hashed:  func(done, total int64) { hashed = append(hashed, [2]int64{done, total}) },
			Deleted: func(p string, size int64) { deleted = append(deleted, fmt.Sprint(p, " ", size)) },
		}
		r, err := RunDelete(t.Context(), o, discard)
		if err != nil {
			t.Fatalf("dry run %v: %v", dry, err)
		}
		// The run goes through the listing in reverse.
		want := []string{"MyWorks/new.jpg kept", "MyWorks/b.jpg deleted", "MyWorks/a.jpg deleted"}
		if got := outcomes(r); !slices.Equal(got, want) {
			t.Errorf("dry run %v: got outcomes %q, want %q", dry, got, want)
		}
		if f := r.Files[0]; f.Reason != plan.KeepNotState || f.Size != 1 {
			t.Errorf("dry run %v: got %+v, want size 1 and reason %q", dry, f, plan.KeepNotState)
		}
		if r.Folders != 0 {
			t.Errorf("dry run %v: got %d folders, want 0: MyWorks keeps new.jpg", dry, r.Folders)
		}
		if !slices.Equal(listed, []string{"1 MyWorks"}) || !slices.Equal(checked, []int{3}) {
			t.Errorf("dry run %v: got Listed %q and Checked %v, want [\"1 MyWorks\"] and [3]", dry, listed, checked)
		}
		// new.jpg has no record, so it is kept without a check.
		if want := []string{"MyWorks/b.jpg", "MyWorks/a.jpg"}; !slices.Equal(hashing, want) {
			t.Errorf("dry run %v: got Hashing %q, want %q", dry, hashing, want)
		}
		if want := [][2]int64{{2, 5}, {5, 5}}; !slices.Equal(hashed, want) {
			t.Errorf("dry run %v: got Hashed %v, want %v", dry, hashed, want)
		}
		var wantDeleted []string
		if !dry {
			wantDeleted = []string{"MyWorks/b.jpg 2", "MyWorks/a.jpg 3"}
		}
		if !slices.Equal(deleted, wantDeleted) {
			t.Errorf("dry run %v: got Deleted %q, want %q", dry, deleted, wantDeleted)
		}
	}
	if s.has(t, "MyWorks/a.jpg") || s.has(t, "MyWorks/b.jpg") || !s.has(t, "MyWorks/new.jpg") {
		t.Error("after the real run: want a.jpg and b.jpg gone and new.jpg kept")
	}
}

// A failed removal is in the result with its reason.
func TestDeleteResultFailed(t *testing.T) {
	s := newShare(t)
	put(t, s.dir, "MyWorks/a.jpg", "a")
	o := s.options()
	o.IgnoreState = true
	s.faults = map[string]remote.Fault{"MyWorks/a.jpg": {BeforeRecheck: func() { s.last.Close() }}}
	r, err := RunDelete(t.Context(), o, discard)
	if err == nil {
		t.Fatal("got no error, want the failed removal")
	}
	if got, want := outcomes(r), []string{"MyWorks/a.jpg failed"}; !slices.Equal(got, want) {
		t.Fatalf("got outcomes %q, want %q", got, want)
	}
	if r.Files[0].Reason != err.Error() {
		t.Errorf("got reason %q, want the run's error %q", r.Files[0].Reason, err)
	}
}

// A local copy that can't be read fails its file, and the run goes on to
// the end and returns ErrFailedFiles, with the CLI's text.
func TestDeleteFailedFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports a file in the path as a missing copy")
	}
	s := newShare(t)
	put(t, s.dir, "MyWorks/a.jpg", "a")
	put(t, s.dir, "Other/b.jpg", "b")
	s.record(t, map[string]string{"MyWorks/a.jpg": "a", "Other/b.jpg": "b"})
	// MyWorks is a file in the destination, so a.jpg's copy can't be read.
	put(t, s.dest, "MyWorks", "not a folder")
	put(t, s.dest, "Other/b.jpg", "b")
	o := s.options()
	o.DryRun = true
	r, err := RunDelete(t.Context(), o, discard)
	if !errors.Is(err, ErrFailedFiles) || err.Error() != "failed files: 1" {
		t.Fatalf("got err %v, want ErrFailedFiles as failed files: 1", err)
	}
	if got, want := outcomes(r), []string{"Other/b.jpg deleted", "MyWorks/a.jpg failed"}; !slices.Equal(got, want) {
		t.Errorf("got outcomes %q, want %q", got, want)
	}
}

// Only limits a real run to the files it holds: nil is every file, empty
// is none, and a file outside it, such as one that became eligible after
// the preview or one in an unchecked folder, is left alone. A file in it
// is still checked.
func TestDeleteOnly(t *testing.T) {
	files := map[string]string{"MyWorks/a.jpg": "a", "MyWorks/late.jpg": "l", "Other/c.jpg": "c", "MyWorks/changed.jpg": "x"}
	for _, tc := range []struct {
		name string
		only map[string]bool
		want []string // the outcomes
		gone []string
	}{
		{"nil", nil, []string{"Other/c.jpg deleted", "MyWorks/late.jpg deleted", "MyWorks/changed.jpg kept", "MyWorks/a.jpg deleted"},
			[]string{"MyWorks/a.jpg", "MyWorks/late.jpg", "Other", "Other/c.jpg"}},
		{"empty", map[string]bool{}, nil, nil},
		{"preview", map[string]bool{"MyWorks/a.jpg": true, "MyWorks/changed.jpg": true},
			[]string{"MyWorks/changed.jpg kept", "MyWorks/a.jpg deleted"}, []string{"MyWorks/a.jpg"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newShare(t)
			for p, data := range files {
				put(t, s.dir, p, data)
			}
			// The state was right for changed.jpg when it was previewed;
			// the device has changed it since.
			s.record(t, map[string]string{"MyWorks/a.jpg": "a", "MyWorks/late.jpg": "l", "Other/c.jpg": "c", "MyWorks/changed.jpg": "xy"})
			o := s.options()
			o.TrustState, o.Only = true, tc.only
			r, err := RunDelete(t.Context(), o, discard)
			if err != nil {
				t.Fatal(err)
			}
			if got := outcomes(r); !slices.Equal(got, tc.want) {
				t.Errorf("got outcomes %q, want %q", got, tc.want)
			}
			for _, p := range []string{"MyWorks/a.jpg", "MyWorks/late.jpg", "MyWorks/changed.jpg", "Other", "Other/c.jpg"} {
				if gone := !s.has(t, p); gone != slices.Contains(tc.gone, p) {
					t.Errorf("%s: gone %v, want %v", p, gone, !gone)
				}
			}
		})
	}
}

// Stop during a removal lets that file finish and count, and starts no
// other file or folder.
func TestDeleteStop(t *testing.T) {
	s := newShare(t)
	for _, p := range []string{"MyWorks/a.jpg", "MyWorks/b.jpg", "MyWorks/c.jpg"} {
		put(t, s.dir, p, "x")
	}
	stop := make(chan struct{})
	// The run goes in reverse: c.jpg, then b.jpg, which Stop holds up.
	s.faults = map[string]remote.Fault{"MyWorks/b.jpg": {BeforeRecheck: func() { close(stop) }}}
	o := s.options()
	o.IgnoreState, o.Stop = true, stop
	var deleted []string
	o.Hooks.Deleted = func(p string, _ int64) { deleted = append(deleted, p) }
	var logs bytes.Buffer
	r, err := RunDelete(t.Context(), o, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"MyWorks/c.jpg", "MyWorks/b.jpg"}
	if !slices.Equal(deleted, want) {
		t.Errorf("Deleted: got %q, want %q", deleted, want)
	}
	if got, want := outcomes(r), []string{"MyWorks/c.jpg deleted", "MyWorks/b.jpg deleted"}; !slices.Equal(got, want) {
		t.Errorf("got outcomes %q, want %q", got, want)
	}
	if calls, _ := s.last.Counts(); calls != 2 || !s.has(t, "MyWorks/a.jpg") || !s.has(t, "MyWorks") {
		t.Errorf("got %d remove calls, want 2, with a.jpg and MyWorks kept", calls)
	}
	if !strings.Contains(logs.String(), `msg="deletion stopped" deleted=2 `) {
		t.Errorf("want a deletion stopped line with deleted=2 in:\n%s", logs.String())
	}
}

// A confirmation proven by the copies checks each file Only holds against
// its local copy again: a copy moved away, corrupted or resized since the
// preview keeps the file. Hashed counts only the bytes the checks read.
func TestDeleteOnlyProof(t *testing.T) {
	s := newShare(t)
	files := map[string]string{
		"MyWorks/a.jpg":    "aaaa",
		"MyWorks/gone.jpg": "gggggggg",
		"MyWorks/bad.jpg":  "bbbbbbbbbbbbbbbb",
		"MyWorks/size.jpg": strings.Repeat("s", 32),
	}
	for p, data := range files {
		put(t, s.dir, p, data)
		put(t, s.dest, p, data)
	}
	s.record(t, files)
	o := s.options()
	o.DryRun = true
	preview, err := RunDelete(t.Context(), o, discard)
	if err != nil {
		t.Fatal(err)
	}
	only := map[string]bool{}
	for _, f := range preview.Files {
		if f.Outcome == FileDeleted {
			only[f.Path] = true
		}
	}
	if len(only) != len(files) {
		t.Fatalf("preview: got outcomes %q, want every file deleted", outcomes(preview))
	}
	// Since the preview, one copy was moved away, one corrupted at the
	// same size and one resized.
	if err := os.Remove(filepath.Join(s.dest, "MyWorks", "gone.jpg")); err != nil {
		t.Fatal(err)
	}
	put(t, s.dest, "MyWorks/bad.jpg", strings.Repeat("x", 16))
	put(t, s.dest, "MyWorks/size.jpg", "s")
	o = s.options()
	o.Only = only
	var hashed [][2]int64
	var hashing []string
	o.Hooks.Hashing = func(p string) { hashing = append(hashing, p) }
	o.Hooks.Hashed = func(done, total int64) { hashed = append(hashed, [2]int64{done, total}) }
	r, err := RunDelete(t.Context(), o, discard)
	if err != nil {
		t.Fatal(err)
	}
	want := []DeleteFile{
		{"MyWorks/size.jpg", 32, FileKept, "local copy size differs from the state"},
		{"MyWorks/gone.jpg", 8, FileKept, "local copy missing; give --trust-state if it was moved"},
		{"MyWorks/bad.jpg", 16, FileKept, "local copy's SHA-256 differs from the state"},
		{"MyWorks/a.jpg", 4, FileDeleted, ""},
	}
	if !slices.Equal(r.Files, want) {
		t.Errorf("got files %+v, want %+v", r.Files, want)
	}
	for _, p := range []string{"MyWorks/size.jpg", "MyWorks/gone.jpg", "MyWorks/bad.jpg"} {
		if !s.has(t, p) {
			t.Errorf("%s: gone from the device, want it kept", p)
		}
	}
	if s.has(t, "MyWorks/a.jpg") {
		t.Error("MyWorks/a.jpg: still on the device, want it deleted")
	}
	// Every file Only holds is checked, read or not.
	if want := []string{"MyWorks/size.jpg", "MyWorks/gone.jpg", "MyWorks/bad.jpg", "MyWorks/a.jpg"}; !slices.Equal(hashing, want) {
		t.Errorf("got Hashing %q, want %q", hashing, want)
	}
	// The resized and missing copies are not read; the corrupted one is.
	if want := [][2]int64{{0, 60}, {0, 60}, {16, 60}, {20, 60}}; !slices.Equal(hashed, want) {
		t.Errorf("got Hashed %v, want %v", hashed, want)
	}
}

// Hashing comes before the copy is read: a copy changed from inside
// Hashing is the one checked, so the file is kept.
func TestDeleteHashingBeforeRead(t *testing.T) {
	s := newShare(t)
	put(t, s.dir, "MyWorks/a.jpg", "aaaa")
	put(t, s.dest, "MyWorks/a.jpg", "aaaa")
	s.record(t, map[string]string{"MyWorks/a.jpg": "aaaa"})
	o := s.options()
	o.Hooks.Hashing = func(p string) { put(t, s.dest, p, "xxxx") }
	r, err := RunDelete(t.Context(), o, discard)
	if err != nil {
		t.Fatal(err)
	}
	if want := []DeleteFile{{"MyWorks/a.jpg", 4, FileKept, "local copy's SHA-256 differs from the state"}}; !slices.Equal(r.Files, want) {
		t.Errorf("got files %+v, want %+v", r.Files, want)
	}
	if !s.has(t, "MyWorks/a.jpg") {
		t.Error("MyWorks/a.jpg: gone from the device, want it kept")
	}
}

// A real run counts the folder it emptied, and a file younger than
// OlderThan is in the result as recent.
func TestDeleteResultFoldersAndRecent(t *testing.T) {
	s := newShare(t)
	put(t, s.dir, "Done/a.jpg", "a")
	put(t, s.dir, "MyWorks/new.jpg", "new")
	now := time.Now()
	if err := os.Chtimes(filepath.Join(s.dir, "MyWorks", "new.jpg"), now, now); err != nil {
		t.Fatal(err)
	}
	o := s.options()
	o.IgnoreState, o.OlderThan = true, 24*time.Hour
	r, err := RunDelete(t.Context(), o, discard)
	if err != nil {
		t.Fatal(err)
	}
	want := []DeleteFile{{"MyWorks/new.jpg", 3, FileRecent, ""}, {"Done/a.jpg", 1, FileDeleted, ""}}
	if !slices.Equal(r.Files, want) {
		t.Errorf("got files %+v, want %+v", r.Files, want)
	}
	if r.Folders != 1 || s.has(t, "Done") || !s.has(t, "MyWorks/new.jpg") {
		t.Errorf("got %d folders, want 1: Done removed and MyWorks/new.jpg kept", r.Folders)
	}
}
