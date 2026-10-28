//go:build gui

package ui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"golang.org/x/sys/windows"
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
		// Engine initialization may open its UDF with delete access. Holding a
		// privatefile.Directory handle here causes ERROR_SHARING_VIOLATION.
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			t.Fatal(err)
		}
		h, err := windows.CreateFile(name, windows.DELETE|windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			t.Fatal("profile blocks engine directory access", err)
		}
		windows.CloseHandle(h)
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
