//go:build windows

package cloudhttp

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"golang.org/x/sys/windows"
)

func TestWindowsPersistentSource(t *testing.T) {
	// All lock holders live in the bubble; file I/O cannot advance its clock.
	synctest.Test(t, testWindowsPersistentSource)
}

func testWindowsPersistentSource(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	for range 16 {
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
			t.Fatal("concurrent identity changed")
		}
	}
	if first == "" {
		t.Fatal("no identity")
	}
	if id, err := persistentSource(dir); err != nil || id != first {
		t.Fatalf("restart %q %v", id, err)
	}
	path := filepath.Join(dir, "InterProcessKV", "device_uuid")
	f, err := privatefile.Open(path, os.O_RDWR|os.O_CREATE)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	assertIdentityHandle(t, windows.Handle(f.Fd()))
	d, err := privatefile.Directory(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	assertIdentityHandle(t, windows.Handle(d.Fd()))
	var lock windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &lock); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := persistentSource(dir); err == nil {
		t.Fatal("contended lock accepted")
	} else if phase, reason := diagnostic.Fields(err, ""); phase != "state_identity" || reason != "lock_timeout" {
		t.Fatalf("contention = %s/%s; want state_identity/lock_timeout", phase, reason)
	}
	if elapsed := time.Since(start); elapsed != time.Second {
		t.Fatalf("lock timeout = %v; want one virtual second", elapsed)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if id, err := persistentSource(dir); err != nil || id != first {
			t.Errorf("queued identity = %q, %v; want %s", id, err, first)
		}
	}()
	synctest.Wait()
	select {
	case <-done:
		t.Fatal("contended identity completed before release")
	default:
	}
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &lock); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestWindowsStateDirectoryADSRefusal(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "state")
	for _, suffix := range []string{":stream", "::$DATA"} {
		if _, err := persistentSource(base + suffix); err == nil {
			t.Fatal("stream identity directory accepted")
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("identity refusal created objects: %v, %v", entries, err)
		}
	}
}
