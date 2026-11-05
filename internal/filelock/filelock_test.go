package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func open(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// A second open file conflicts in the same process, until the first is
// unlocked or closed.
func TestTryLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	first, second := open(t, path), open(t, path)
	if ok, err := TryLock(first); !ok || err != nil {
		t.Fatalf("first TryLock = %v, %v", ok, err)
	}
	if ok, err := TryLock(second); ok || err != nil {
		t.Fatalf("second TryLock while held = %v, %v; want false, nil", ok, err)
	}
	if err := Unlock(first); err != nil {
		t.Fatal(err)
	}
	if ok, err := TryLock(second); !ok || err != nil {
		t.Fatalf("TryLock after Unlock = %v, %v", ok, err)
	}
	second.Close()
	if ok, err := TryLock(open(t, path)); !ok || err != nil {
		t.Fatalf("TryLock after Close = %v, %v", ok, err)
	}
}

// Lock without a timeout blocks until the holder lets go.
func TestLockWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	first := open(t, path)
	if err := Lock(first, 0); err != nil {
		t.Fatal(err)
	}
	second := open(t, path)
	got := make(chan error, 1)
	go func() { got <- Lock(second, 0) }()
	select {
	case err := <-got:
		t.Fatalf("Lock returned %v while the lock was held", err)
	case <-time.After(50 * time.Millisecond):
	}
	first.Close()
	if err := <-got; err != nil {
		t.Errorf("Lock after the release: %v", err)
	}
}

// Lock with a timeout gives up after exactly the timeout, and takes a lock
// freed before then.
func TestLockTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	first, second := open(t, path), open(t, path)
	if ok, err := TryLock(first); !ok || err != nil {
		t.Fatal(ok, err)
	}
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		if err := Lock(second, time.Second); !errors.Is(err, ErrTimeout) {
			t.Fatalf("Lock = %v, want ErrTimeout", err)
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("Lock gave up after %v, want 1s", elapsed)
		}
		go func() {
			time.Sleep(500 * time.Millisecond)
			Unlock(first)
		}()
		if err := Lock(second, time.Second); err != nil {
			t.Errorf("Lock of a lock freed in time = %v", err)
		}
	})
}
