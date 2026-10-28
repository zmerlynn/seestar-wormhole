//go:build gui

package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
)

func TestWindowProfileCleanup(t *testing.T) {
	st := testStorage(t)
	for i := 0; i < 2; i++ {
		path, cleanup, err := windowProfile(st.root)
		if err != nil {
			t.Fatal(err)
		}
		if path != filepath.Join(st.root, "webview") {
			t.Fatal("profile outside private state root")
		}
		dir, err := privatefile.ReadDirectory(path)
		if err != nil {
			t.Fatal(err)
		}
		dir.Close()
		f, err := privatefile.Open(filepath.Join(path, "cache"), os.O_CREATE|os.O_EXCL|os.O_WRONLY)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("profile survived cleanup", err)
		}
	}
}
