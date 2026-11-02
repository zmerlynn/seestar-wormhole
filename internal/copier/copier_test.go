package copier

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// recordedAt is the at time the tests' copiers record.
var recordedAt = time.Date(2026, 11, 2, 1, 2, 3, 0, time.UTC)

// fixture is a destination with its state open.
type fixture struct {
	dest      string
	statePath string
	st        *state.State
}

// newFixture opens the state of a new destination. prepare, if not nil,
// runs on the destination before the state and the copier exist.
func newFixture(t *testing.T, prepare func(dest string)) fixture {
	t.Helper()
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	if err := os.Mkdir(dest, 0o777); err != nil {
		t.Fatal(err)
	}
	if prepare != nil {
		prepare(dest)
	}
	stateDir := filepath.Join(base, "state")
	st, err := state.Open(stateDir, dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	statePath, err := state.Path(stateDir, dest)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{dest: dest, statePath: statePath, st: st}
}

// copier returns a Copier on the fixture's destination that records
// recordedAt.
func (fx fixture) copier(t *testing.T) *Copier {
	t.Helper()
	c, err := New(fx.dest, fx.st, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return recordedAt }
	t.Cleanup(func() { c.Close() })
	return c
}

func writeFile(t *testing.T, name, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), 0o666); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("cannot create a symbolic link: %v", err)
	}
}

// New removes leftover temporary files at any depth, and nothing else: not
// a name that is close to the pattern, not a folder or a symbolic link with
// a temporary name, and nothing behind a symbolic link.
// The other-case name has a folder of its own, since on a case-insensitive
// filesystem it is the same entry as the folder with the temporary name.
func TestNewRemovesLeftoverTempFiles(t *testing.T) {
	tmpA := plan.TempName("a.fit")
	tmpB := plan.TempName("MyWorks/M 31/b.fit")
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, tmpA), "outside")
	writeFile(t, filepath.Join(outside, "target"), "outside")
	kept := map[string]string{
		"MyWorks/a.fit":                          "kept",
		".seestar-copy-tmp/0123abcd":             "old temporary folder",
		"Upper/" + strings.ToUpper(tmpA):         "other case",
		"MyWorks/" + tmpA[:len(tmpA)-5] + ".tmp": "63 digits",
		"MyWorks/" + tmpA + ".fit":               "longer",
		"MyWorks/x" + tmpA:                       "prefixed",
		"MyWorks/" + tmpA + "/inside":            "in a folder with a temporary name",
	}
	fx := newFixture(t, func(dest string) {
		writeFile(t, filepath.Join(dest, tmpA), "partial")
		writeFile(t, filepath.Join(dest, "MyWorks", "M 31", tmpB), "partial")
		writeFile(t, filepath.Join(dest, "a", "b", "c", "d", tmpA), "partial")
		for name, data := range kept {
			writeFile(t, filepath.Join(dest, filepath.FromSlash(name)), data)
		}
		symlink(t, filepath.Join(outside, "target"), filepath.Join(dest, "MyWorks", tmpB))
		symlink(t, outside, filepath.Join(dest, "Linked"))
	})
	fx.copier(t)
	for _, name := range []string{tmpA, "MyWorks/M 31/" + tmpB, "a/b/c/d/" + tmpA} {
		fx.checkAbsent(t, name)
	}
	for name, want := range kept {
		if got, err := os.ReadFile(filepath.Join(fx.dest, filepath.FromSlash(name))); err != nil || string(got) != want {
			t.Errorf("%s after New: got %q, %v; want %q", name, got, err, want)
		}
	}
	if fi, err := os.Lstat(filepath.Join(fx.dest, "MyWorks", tmpB)); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("link with a temporary name after New: got %v, %v; want the link kept", fi, err)
	}
	for _, name := range []string{tmpA, "target"} {
		if got, err := os.ReadFile(filepath.Join(outside, name)); err != nil || string(got) != "outside" {
			t.Errorf("%s outside the destination after New: got %q, %v; want %q", name, got, err, "outside")
		}
	}
}

func TestNewMissingDest(t *testing.T) {
	fx := newFixture(t, nil)
	if _, err := New(filepath.Join(fx.dest, "missing"), fx.st, nil); err == nil {
		t.Error("New of a missing destination: got nil error")
	}
}

func TestStat(t *testing.T) {
	mtime := time.Date(2026, 10, 29, 3, 31, 19, 0, time.UTC)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "a.fit"), "outside")
	fx := newFixture(t, func(dest string) {
		writeFile(t, filepath.Join(dest, "MyWorks", "a.fit"), "12345")
		if err := os.Chtimes(filepath.Join(dest, "MyWorks", "a.fit"), time.Time{}, mtime); err != nil {
			t.Fatal(err)
		}
		symlink(t, "a.fit", filepath.Join(dest, "MyWorks", "link.fit"))
		symlink(t, outside, filepath.Join(dest, "Escape"))
	})
	c := fx.copier(t)

	if got := c.Stat("MyWorks/a.fit"); !got.Exists || !got.Regular || got.Size != 5 || !got.ModTime.Equal(mtime) || got.Err != nil {
		t.Errorf("Stat(MyWorks/a.fit) = %+v, want an existing regular file of 5 bytes with mtime %v", got, mtime)
	}
	if got := c.Stat("MyWorks/missing.fit"); got.Exists || got.Err != nil {
		t.Errorf("Stat(MyWorks/missing.fit) = %+v, want not existing and no error", got)
	}
	if got := c.Stat("MyWorks"); !got.Exists || got.Regular || got.Err != nil {
		t.Errorf("Stat(MyWorks) = %+v, want an existing non-regular entry", got)
	}
	if got := c.Stat("MyWorks/link.fit"); !got.Exists || got.Regular || got.Err != nil {
		t.Errorf("Stat(MyWorks/link.fit) = %+v, want the link itself, not regular", got)
	}
	if got := c.Stat("Escape/a.fit"); got.Err == nil {
		t.Errorf("Stat(Escape/a.fit) through a link out of the destination = %+v, want an error", got)
	}
}

// deniedFS fails reading the folder deny with err.
type deniedFS struct {
	fs.FS
	deny string
	err  error
}

func (f deniedFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == f.deny {
		return nil, &fs.PathError{Op: "open", Path: name, Err: f.err}
	}
	return fs.ReadDir(f.FS, name)
}

// New skips, with a warning, a folder it is not allowed to read, such as a
// lost+found at the top of a mount: sync cannot have written there. Any
// other error, or a destination it cannot read at all, fails New.
func TestNewSkipsUnreadableFolder(t *testing.T) {
	tmp := plan.TempName("a.fit")
	for _, tc := range []struct {
		name string
		deny string
		err  error
		want string // what New's error contains; "" for success
	}{
		{name: "permission denied", deny: "MyWorks/lost+found", err: fs.ErrPermission},
		{name: "permission denied at the top", deny: ".", err: fs.ErrPermission, want: "remove leftover temporary files in"},
		{name: "other error", deny: "MyWorks/lost+found", err: syscall.EIO, want: "open MyWorks/lost+found: input/output error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := rootFS
			rootFS = func(r *os.Root) fs.FS { return deniedFS{FS: orig(r), deny: tc.deny, err: tc.err} }
			t.Cleanup(func() { rootFS = orig })
			fx := newFixture(t, func(dest string) {
				for _, dir := range []string{".", "MyWorks", "MyWorks/lost+found", "Other"} {
					writeFile(t, filepath.Join(dest, filepath.FromSlash(dir), tmp), "partial")
				}
			})
			var logged strings.Builder
			c, err := New(fx.dest, fx.st, slog.New(slog.NewTextHandler(&logged, nil)))
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.Is(err, tc.err) {
					t.Errorf("New: got err %v, want one containing %q and wrapping %v", err, tc.want, tc.err)
				}
				if c != nil {
					c.Close()
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			c.Close()
			for _, dir := range []string{".", "MyWorks", "Other"} {
				fx.checkAbsent(t, path.Join(dir, tmp))
			}
			if _, err := os.Stat(filepath.Join(fx.dest, "MyWorks", "lost+found", tmp)); err != nil {
				t.Errorf("temporary file in the skipped folder: %v", err)
			}
			if want := `level=WARN msg="skipped a folder that cannot be read while removing leftover temporary files" folder=MyWorks/lost+found reason="open MyWorks/lost+found: permission denied"`; !strings.Contains(logged.String(), want) {
				t.Errorf("logged:\n%s\nwant a line containing\n%s", logged.String(), want)
			}
		})
	}
}

// The same with a real folder that cannot be read; the test cannot run as
// root, who can read it.
func TestNewSkipsUnreadableFolderOnDisk(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder this user cannot read")
	}
	tmp := plan.TempName("a.fit")
	var locked string
	fx := newFixture(t, func(dest string) {
		writeFile(t, filepath.Join(dest, tmp), "partial")
		locked = filepath.Join(dest, "lost+found")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o700) })
	})
	c, err := New(fx.dest, fx.st, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.Close()
	fx.checkAbsent(t, tmp)
}

// A final file reached through a symbolic link to another folder in the
// destination is not removed by the next New, and the next pass finds it
// done.
func TestNewKeepsFileBehindInternalLink(t *testing.T) {
	const p = "MyWorks/a.fit"
	fx := newFixture(t, func(dest string) {
		if err := os.Mkdir(filepath.Join(dest, "Other"), 0o777); err != nil {
			t.Fatal(err)
		}
		symlink(t, "Other", filepath.Join(dest, "MyWorks"))
	})
	c := fx.copier(t)
	listing := []plan.Entry{{Path: p, Size: 3, ModTime: deviceMTime, Regular: true}}
	in := plan.Input{Listing: listing, Now: recordedAt, FirstPass: true, Local: map[string]plan.Local{}}
	for _, q := range plan.NeedLocal(in) {
		in.Local[q] = c.Stat(q)
	}
	ds, _ := plan.Decide(in)
	results := run(c, t.Context(), device(map[string]string{p: "abc"}), 1, ds)
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("got results %+v, want %s copied", results, p)
	}
	c.Close()
	fx.copier(t)
	fx.checkFile(t, p, "abc")
	fx.checkFile(t, "Other/a.fit", "abc")
	rec, ok := fx.st.Lookup(p)
	if !ok {
		t.Fatalf("%s is not recorded", p)
	}
	in = plan.Input{Listing: listing, Now: recordedAt, FirstPass: true, State: map[string]plan.Recorded{p: {Size: rec.Size, ModTime: rec.MTime}}}
	if ds, _ := plan.Decide(in); len(ds) != 1 || ds[0].Kind != plan.Done {
		t.Errorf("next pass: got %+v, want %s done", ds, p)
	}
}
