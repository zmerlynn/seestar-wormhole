//go:build gui

package ui

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
)

func windowProfile(root string) (string, func() error, error) {
	path, err := filepath.Abs(filepath.Join(root, "webview"))
	if err != nil {
		return "", nil, errStorage
	}
	// Validate before removing any leftovers from an interrupted previous run.
	dir, err := privatefile.Directory(path)
	if err != nil {
		return "", nil, errStorage
	}
	dir.Close()
	if err := os.RemoveAll(path); err != nil {
		return "", nil, errStorage
	}
	dir, err = privatefile.Directory(path)
	if err != nil {
		return "", nil, errStorage
	}
	// The private state root remains pinned by storage. Do not pin the engine's
	// directory without FILE_SHARE_DELETE: WebView2 must manage its own folder.
	dir.Close()
	return path, func() error {
		// WebView2 may release its profile locks shortly after the window closes.
		deadline := time.Now().Add(2 * time.Second)
		for {
			if err := os.RemoveAll(path); err == nil {
				return nil
			}
			if time.Now().After(deadline) {
				return errors.New("WebView2 profile cleanup failed: remove webview from the private state directory after exit")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}, nil
}
