//go:build gui

package ui

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
)

func windowProfile(root string) (string, func() error, error) {
	// Only the owner holding ui.lock calls this, before creating its own profile.
	sweepWindowProfiles(root)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", nil, errStorage
	}
	path, err := filepath.Abs(filepath.Join(root, "webview-"+hex.EncodeToString(nonce[:])))
	if err != nil {
		return "", nil, errStorage
	}
	dir, err := privatefile.Directory(path)
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
				return errors.New("WebView2 profile cleanup failed: a later launch will retry removing stale profiles")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}, nil
}

// Disposable profiles alone may be left locked by an engine child. Never reuse
// them or let failed cleanup prevent launch. Other state keeps its strict checks.
func sweepWindowProfiles(root string) {
	entries, _ := os.ReadDir(root)
	for _, entry := range entries {
		name := entry.Name()
		nonce, err := hex.DecodeString(strings.TrimPrefix(name, "webview-"))
		if name != "webview" && (!strings.HasPrefix(name, "webview-") || err != nil || len(nonce) != 16) {
			continue
		}
		path := filepath.Join(root, name)
		dir, err := privatefile.ReadDirectory(path)
		if err != nil { // Includes unsafe ACLs, reparse points, and sharing violations.
			continue
		}
		dir.Close()
		_ = os.RemoveAll(path)
	}
}
