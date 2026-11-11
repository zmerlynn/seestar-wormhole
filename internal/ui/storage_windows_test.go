package ui

import (
	"bytes"
	"encoding/json/v2"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"golang.org/x/sys/windows"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateStorageWindows(t *testing.T) {
	st := testStorage(t)
	if err := st.write("config.json", testSettings()); err != nil {
		t.Fatal(err)
	}
	reader, err := privatefile.OpenRead(filepath.Join(st.root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := st.write("config.json", defaultSettings()); err != nil {
		t.Fatal("atomic replacement", err)
	}
	old, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(testSettings())
	if !bytes.Equal(old, want) {
		t.Fatal("reader lost the original contents")
	}
	got, _, err := loadSettings(st)
	if err != nil || got.Email != defaultSettings().Email {
		t.Fatalf("replacement not visible: %v", err)
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

// copy.json goes through the same private storage as config.json.
func TestCopySettingsPrivateWindows(t *testing.T) {
	st := testStorage(t)
	c, _, _, err := loadCopySettings(st)
	if err != nil {
		t.Fatal(err)
	}
	set := copyapp.DefaultSettings()
	set.Folder = `C:\synthetic\Seestar`
	if err := c.save(set); err != nil {
		t.Fatal(err)
	}
	f, err := privatefile.Open(filepath.Join(st.root, copyName), os.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := privatefile.Check(windows.Handle(f.Fd()), false); err != nil {
		t.Fatal(err)
	}
}

func TestLogPrivateACLWindows(t *testing.T) {
	st := testStorage(t)
	log := &eventLog{storage: st}
	for range 700 {
		log.add("Connecting to your Seestar…")
		log.add("Stopped")
	}
	for _, name := range []string{"gui.log", "gui.log.1"} {
		f, err := privatefile.OpenRead(filepath.Join(st.root, name))
		if err != nil {
			t.Fatal(err)
		}
		err = privatefile.Check(windows.Handle(f.Fd()), false)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
