package ui

import (
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateStorageWindows(t *testing.T) {
	st := testStorage(t)
	if err := st.write("config.json", testSettings()); err != nil {
		t.Fatal(err)
	}
	if err := st.write("config.json", defaultSettings()); err != nil {
		t.Fatal("atomic replacement", err)
	}
	f, err := privatefile.Open(filepath.Join(st.root, "config.json"), os.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := privatefile.Check(windows.Handle(f.Fd()), false); err != nil {
		t.Fatal(err)
	}
	if err := privatefile.Check(windows.Handle(st.directory.Fd()), true); err != nil {
		t.Fatal(err)
	}
}
