//go:build unix

package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHeadlessSettingsPrivateFiles(t *testing.T) {
	st := testStorage(t)
	path := filepath.Join(st.root, "config.json")
	if err := st.write("config.json", testSettings()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(st.root); err == nil {
		t.Fatal("public config accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte(`{"version":2}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(st.root); err == nil {
		t.Fatal("symlink config accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := st.write("config.json", testSettings()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(st.root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(st.root); err == nil {
		t.Fatal("public root accepted")
	}
}
