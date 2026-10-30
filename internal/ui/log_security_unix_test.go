//go:build unix

package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogUnixModesAndSymlinkRefusal(t *testing.T) {
	st := testStorage(t)
	log := &eventLog{storage: st}
	for range 700 {
		log.add("Connecting to your Seestar…")
		log.add("Stopped")
	}
	for _, name := range []string{"gui.log", "gui.log.1"} {
		info, err := os.Stat(filepath.Join(st.root, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("unsafe Unix log mode", err)
		}
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gui.log", "gui.log.1"} {
		st := testStorage(t)
		log := &eventLog{storage: st}
		if err := os.Symlink(sentinel, filepath.Join(st.root, name)); err != nil {
			t.Fatal(err)
		}
		for range 700 {
			log.add("Connecting to your Seestar…")
			log.add("Stopped")
		}
		if _, _, failed := log.snapshot(); !failed {
			t.Fatal("symlink failure not surfaced")
		}
		got, err := os.ReadFile(sentinel)
		if err != nil || string(got) != "unchanged" {
			t.Fatal("log followed symlink")
		}
	}
}
