//go:build gui

package ui

import (
	"os"
	"path/filepath"
	"strings"
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
		if filepath.Dir(path) != st.root || !strings.HasPrefix(filepath.Base(path), "webview-") {
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

func TestWindowProfileLockedRelaunch(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "per-launch", true: "legacy"}[legacy], func(t *testing.T) {
			st := testStorage(t)
			previous, cleanup, err := windowProfile(st.root)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				old := filepath.Join(st.root, "webview")
				if err := os.Rename(previous, old); err != nil {
					t.Fatal(err)
				}
				previous = old
			}
			// A real Windows handle denies deletion, as a surviving engine child does.
			f, err := privatefile.Open(filepath.Join(previous, "cache"), os.O_CREATE|os.O_EXCL|os.O_RDWR)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { f.Close() })
			dir, err := privatefile.Directory(previous)
			if err != nil {
				t.Fatal("locked child unexpectedly failed profile validation", err)
			}
			dir.Close()
			if err := os.RemoveAll(previous); err == nil {
				t.Fatal("test handle did not lock the previous profile")
			}
			// Prove the root's validator still accepts state with a locked profile.
			reopened, err := openStorage(st.root)
			if err != nil {
				t.Fatal("state root rejected locked engine content", err)
			}
			reopened.directory.Close()
			next, nextCleanup, err := windowProfile(st.root)
			if err != nil {
				t.Fatal("locked stale profile blocked launch", err)
			}
			defer nextCleanup()
			if next == previous {
				t.Fatal("reused a stale profile")
			}
			if _, err := os.Stat(previous); err != nil {
				t.Fatal("locked profile should remain", err)
			}
			f.Close()
			last, lastCleanup, err := windowProfile(st.root)
			if err != nil {
				t.Fatal(err)
			}
			defer lastCleanup()
			if last == next || last == previous {
				t.Fatal("launch did not allocate a new profile")
			}
			if _, err := os.Stat(previous); !os.IsNotExist(err) {
				t.Fatal("unlocked stale profile survived next launch", err)
			}
			if err := cleanup(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWindowProfileSweepScope(t *testing.T) {
	st := testStorage(t)
	// The prefix alone never authorizes deletion of arbitrary state.
	for _, name := range []string{"config.json", "ui.lock", "webview-not-a-profile", "webview-00000000000000000000000000000000"} {
		f, err := privatefile.Open(filepath.Join(st.root, name), os.O_CREATE|os.O_EXCL|os.O_RDWR)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	unsafe := filepath.Join(st.root, "webview-11111111111111111111111111111111")
	if err := os.Mkdir(unsafe, 0700); err != nil {
		t.Fatal(err)
	}
	if dir, err := privatefile.ReadDirectory(unsafe); err == nil {
		dir.Close()
		t.Fatal("fixture unexpectedly has a protected private DACL")
	}
	path, cleanup, err := windowProfile(st.root)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	entries, err := os.ReadDir(st.root)
	if err != nil || len(entries) != 6 {
		t.Fatal("sweep removed non-profile state", err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}
