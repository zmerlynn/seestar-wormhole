//go:build unix

package ui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateStorageModesAndRefusals(t *testing.T) {
	st := testStorage(t)
	if err := st.write("config.json", testSettings()); err != nil {
		t.Fatal(err)
	}
	if err := st.write("config.json", defaultSettings()); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{st.root: 0700, filepath.Join(st.root, "config.json"): 0600} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("mode %s: %v %v", path, info, err)
		}
	}
	path := filepath.Join(st.root, "config.json")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadSettings(st); err == nil {
		t.Fatal("public settings read")
	}
	if err := st.write("config.json", defaultSettings()); err == nil {
		t.Fatal("public settings overwritten")
	}
	os.Remove(path)
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	os.WriteFile(sentinel, []byte("sentinel"), 0600)
	if err := os.Symlink(sentinel, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadSettings(st); err == nil {
		t.Fatal("symlink read")
	}
	if err := st.write("config.json", defaultSettings()); err == nil {
		t.Fatal("symlink overwritten")
	}
	b, _ := os.ReadFile(sentinel)
	if string(b) != "sentinel" {
		t.Fatal("symlink target changed")
	}
	unsafe := filepath.Join(t.TempDir(), "unsafe")
	os.Mkdir(unsafe, 0755)
	// The refusal is errStorage, as the page's code takes it, and names
	// the folder and what is wrong with it.
	if _, err := openStorage(unsafe); err == nil {
		t.Fatal("public directory accepted")
	} else if want := "Settings storage unavailable: open " + unsafe + ": private file: unsafe permissions or type. Use a private state directory."; !errors.Is(err, errStorage) || err.Error() != want {
		t.Fatalf("refusal = %v; want %q", err, want)
	} else if e := actionError(err); e.Code != "storage" || e.Err != err {
		t.Fatalf("action error = %+v", e)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	os.Symlink(st.root, alias)
	if _, err := openStorage(alias); err == nil {
		t.Fatal("directory symlink accepted")
	}
}
