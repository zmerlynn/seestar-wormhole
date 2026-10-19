//go:build unix

package cloudhttp

import (
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
)

func TestPersistentSource(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			id, err := persistentSource(dir)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		})
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("concurrent source identity changed")
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(first) {
		t.Fatal("UUID v4 shape")
	}
	path := filepath.Join(dir, "InterProcessKV", "device_uuid")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("source mode")
	}
	id, err := persistentSource(dir)
	if err != nil || id != first {
		t.Fatal("restart identity changed")
	}
	// Nonempty values are reused verbatim, not parsed/replaced as UUIDs.
	if err := os.WriteFile(path, []byte("legacy-source"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err = persistentSource(dir)
	if err != nil || id != "legacy-source" {
		t.Fatal("nonempty source replaced")
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	id, err = persistentSource(dir)
	if err != nil || id == "" {
		t.Fatal("empty source not initialized")
	}
}
func TestSourceRejectsUnsafeStorage(t *testing.T) {
	for _, mode := range []string{"symlink", "permissions", "oversized", "directory"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			sub := filepath.Join(dir, "InterProcessKV")
			if err := os.Mkdir(sub, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(sub, "device_uuid")
			var err error
			switch mode {
			case "symlink":
				err = os.Symlink(filepath.Join(dir, "password-sentinel"), path)
			case "permissions":
				err = os.WriteFile(path, []byte("token-sentinel"), 0644)
			case "oversized":
				err = os.WriteFile(path, make([]byte, 4097), 0600)
			case "directory":
				err = os.Mkdir(path, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := persistentSource(dir); err == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
}
