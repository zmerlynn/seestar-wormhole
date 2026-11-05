package privatefile

import (
	"errors"
	"io"
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

// WriteFile publishes the whole content after its fsync, leaves no temporary
// file, and writes a file OpenRead accepts as private.
func TestWriteFile(t *testing.T) {
	d := privateDir(t)
	path := filepath.Join(d.Name(), "state.json")
	var events []string
	oldSync, oldReplace := syncFile, replace
	t.Cleanup(func() { syncFile, replace = oldSync, oldReplace })
	syncFile = func(f *os.File) error {
		events = append(events, "sync")
		if filepath.Dir(f.Name()) != d.Name() || f.Name() == path {
			t.Errorf("fsync of %s, want a temporary file next to %s", f.Name(), path)
		}
		return oldSync(f)
	}
	replace = func(from, to string) error {
		events = append(events, "replace")
		if to != path {
			t.Errorf("replace(%q, %q), want %q", from, to, path)
		}
		return oldReplace(from, to)
	}
	for _, content := range []string{"first", "second, longer"} {
		if err := WriteFile(d, "state.json", []byte(content)); err != nil {
			t.Fatal(err)
		}
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
	if want := []string{"sync", "replace", "sync", "replace"}; !slices.Equal(events, want) {
		t.Errorf("events = %q, want %q", events, want)
	}
	if got := entries(t, d); !slices.Equal(got, []string{"state.json"}) {
		t.Errorf("directory holds %q, want only the file", got)
	}
}

// A failed fsync publishes nothing and removes the temporary file.
func TestWriteFileSyncFailure(t *testing.T) {
	d := privateDir(t)
	if err := WriteFile(d, "state.json", []byte("old")); err != nil {
		t.Fatal(err)
	}
	old := syncFile
	t.Cleanup(func() { syncFile = old })
	syncFile = func(*os.File) error { return errors.New("injected fsync failure") }
	if err := WriteFile(d, "state.json", []byte("new")); err == nil || err.Error() != "injected fsync failure" {
		t.Fatalf("WriteFile = %v, want the fsync error", err)
	}
	if b, err := os.ReadFile(filepath.Join(d.Name(), "state.json")); err != nil || string(b) != "old" {
		t.Errorf("file = %q (%v), want the old content", b, err)
	}
	if got := entries(t, d); !slices.Equal(got, []string{"state.json"}) {
		t.Errorf("directory holds %q, want only the file", got)
	}
}
