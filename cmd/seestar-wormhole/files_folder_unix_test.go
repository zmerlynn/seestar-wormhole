//go:build unix

package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lockedBuffer is a stderr the program and the test share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A folder removed under a running sync fails each later pass without
// connecting, sweeping or writing, and the run goes on until it is stopped.
// The share is a loopback listener that counts connections and closes them,
// so every pass that dials fails at the handshake.
func TestBinarySyncRemovedFolder(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var conns atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			c.Close()
		}
	}()
	dest := filepath.Join(t.TempDir(), "Seestar")
	cmd := exec.Command(binaryPath(t), "sync", "--endpoint", ln.Addr().String(), "--dest", dest, "--history-dir", t.TempDir(), "--interval", "1s")
	cmd.Dir = t.TempDir()
	stderr := &lockedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-done
	})
	waitLog := func(what string, n int) {
		t.Helper()
		for deadline := time.Now().Add(60 * time.Second); strings.Count(stderr.String(), what) < n; time.Sleep(20 * time.Millisecond) {
			select {
			case <-done:
				t.Fatalf("sync exited (%v) before %d of %q:\n%s", waitErr, n, what, stderr.String())
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %d of %q:\n%s", n, what, stderr.String())
			}
		}
	}
	waitLog(`msg="pass failed"`, 1)
	seen := conns.Load()
	if seen == 0 {
		t.Fatalf("the first pass did not connect:\n%s", stderr.String())
	}
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
	waitLog(`level=WARN msg="pass failed" reason=destination_missing dest=`+dest, 2)
	if n := conns.Load(); n != seen {
		t.Errorf("%d connections after the folder was removed, want none", n-seen)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the folder came back: %v", err)
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if waitErr != nil {
			t.Errorf("sync after a stop: %v, want exit 0:\n%s", waitErr, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("sync did not stop")
	}
}
