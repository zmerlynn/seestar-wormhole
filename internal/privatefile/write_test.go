package privatefile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func privateDir(t *testing.T) *os.File {
	t.Helper()
	d, err := Directory(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func entries(t *testing.T, d *os.File) []string {
	t.Helper()
	des, err := os.ReadDir(d.Name())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, de := range des {
		names = append(names, de.Name())
	}
	return names
}

// hookWrite makes WriteFile's fsyncs, replacement and directory sync call the
// given functions first for the rest of the test; a nil one is not hooked,
// and an error fails that step.
func hookWrite(t *testing.T, sync func(*os.File) error, rep func(from, to string) error, dirSync func(*os.File) error) {
	t.Helper()
	oldSync, oldReplace, oldDirSync := syncFile, replace, syncDirectory
	t.Cleanup(func() { syncFile, replace, syncDirectory = oldSync, oldReplace, oldDirSync })
	if sync != nil {
		syncFile = func(f *os.File) error {
			if err := sync(f); err != nil {
				return err
			}
			return oldSync(f)
		}
	}
	if rep != nil {
		replace = func(from, to string, p Policy) error {
			if err := rep(from, to); err != nil {
				return err
			}
			return oldReplace(from, to, p)
		}
	}
	if dirSync != nil {
		syncDirectory = func(f *os.File) error {
			if err := dirSync(f); err != nil {
				return err
			}
			return oldDirSync(f)
		}
	}
}

// WriteFile fsyncs the whole content in a temporary file next to the name
// before anything is published, closes it, replaces the name with it, syncs
// the directory last, leaves no temporary file, and writes a file OpenRead
// accepts as private.
func TestWriteFile(t *testing.T) {
	t.Run("private", func(t *testing.T) { testWriteFile(t, Private) })
	t.Run("inherited", func(t *testing.T) { testWriteFile(t, Inherited) })
}

func testWriteFile(t *testing.T, p Policy) {
	d := privateDir(t)
	path := filepath.Join(d.Name(), "state.json")
	var events []string
	var tmp *os.File
	content, published := "", ""
	hookWrite(t, func(f *os.File) error {
		events = append(events, "sync")
		tmp = f
		if filepath.Dir(f.Name()) != d.Name() || f.Name() == path {
			t.Errorf("fsync of %s, want a temporary file next to %s", f.Name(), path)
		}
		if b, err := os.ReadFile(f.Name()); err != nil || string(b) != content {
			t.Errorf("temporary file at fsync = %q (%v), want %q", b, err, content)
		}
		// Nothing is published yet.
		b, err := os.ReadFile(path)
		if published == "" && !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("file exists at fsync: %q (%v)", b, err)
		} else if published != "" && (err != nil || string(b) != published) {
			t.Errorf("file at fsync = %q (%v), want %q unchanged", b, err, published)
		}
		return nil
	}, func(from, to string) error {
		events = append(events, "replace")
		if tmp == nil || from != tmp.Name() || to != path {
			t.Errorf("replace(%q, %q), want the fsynced temporary file replacing %q", from, to, path)
		}
		if tmp != nil {
			// Seek, unlike Stat, reports a closed file on Windows too.
			if _, err := tmp.Seek(0, io.SeekCurrent); !errors.Is(err, os.ErrClosed) {
				t.Errorf("temporary file still open at replace: %v", err)
			}
		}
		return nil
	}, func(f *os.File) error {
		events = append(events, "sync dir")
		if f != d {
			t.Errorf("directory sync of %s, want %s", f.Name(), d.Name())
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != content {
			t.Errorf("file at directory sync = %q (%v), want %q", b, err, content)
		}
		return nil
	})
	for _, content = range []string{"first", "second, longer"} {
		tmp = nil
		if err := WriteFile(d, "state.json", []byte(content), p); err != nil {
			t.Fatal(err)
		}
		published = content
		f, err := OpenRead(path)
		if err != nil {
			t.Fatalf("OpenRead of the written file: %v", err)
		}
		b, err := io.ReadAll(f)
		f.Close()
		if err != nil || string(b) != content {
			t.Errorf("file = %q (%v), want %q", b, err, content)
		}
		if runtime.GOOS != "windows" {
			if fi, err := os.Lstat(path); err != nil || fi.Mode() != 0600 {
				t.Errorf("mode = %v (%v), want -rw-------", fi.Mode(), err)
			}
		}
	}
	if want := []string{"sync", "replace", "sync dir", "sync", "replace", "sync dir"}; !slices.Equal(events, want) {
		t.Errorf("events = %q, want %q", events, want)
	}
	if got := entries(t, d); !slices.Equal(got, []string{"state.json"}) {
		t.Errorf("directory holds %q, want only the file", got)
	}
}

// A failed directory sync is returned.
func TestWriteFileDirectorySyncFailure(t *testing.T) {
	d := privateDir(t)
	hookWrite(t, nil, nil, func(*os.File) error { return errors.New("injected directory sync failure") })
	if err := WriteFile(d, "state.json", []byte("new"), Private); err == nil || err.Error() != "injected directory sync failure" {
		t.Fatalf("WriteFile = %v, want the directory sync error", err)
	}
}

// A failed fsync publishes nothing and removes the temporary file.
func TestWriteFileSyncFailure(t *testing.T) {
	d := privateDir(t)
	if err := WriteFile(d, "state.json", []byte("old"), Private); err != nil {
		t.Fatal(err)
	}
	hookWrite(t, func(*os.File) error { return errors.New("injected fsync failure") }, nil, nil)
	if err := WriteFile(d, "state.json", []byte("new"), Private); err == nil || err.Error() != "injected fsync failure" {
		t.Fatalf("WriteFile = %v, want the fsync error", err)
	}
	if b, err := os.ReadFile(filepath.Join(d.Name(), "state.json")); err != nil || string(b) != "old" {
		t.Errorf("file = %q (%v), want the old content", b, err)
	}
	if got := entries(t, d); !slices.Equal(got, []string{"state.json"}) {
		t.Errorf("directory holds %q, want only the file", got)
	}
}

// Private refuses to replace a file that is not private, whose DACL Replace
// would keep on Windows, and leaves it as it was; Inherited replaces it.
func TestWriteFilePrivateRefusesUnsafeFile(t *testing.T) {
	d, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	path := filepath.Join(d.Name(), "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(d, "state.json", []byte("new"), Private); err == nil {
		t.Fatal("WriteFile replaced a file that is not private")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "old" {
		t.Errorf("file = %q (%v), want the old content", b, err)
	}
	if got := entries(t, d); !slices.Equal(got, []string{"state.json"}) {
		t.Errorf("directory holds %q, want only the file", got)
	}
	if err := WriteFile(d, "state.json", []byte("new"), Inherited); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "new" {
		t.Errorf("file = %q (%v), want the new content", b, err)
	}
}
