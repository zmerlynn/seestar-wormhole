//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// fullPipe returns the write end of a pipe nobody reads, already full and
// blocking, as a stalled terminal or log collector leaves stderr.
func fullPipe(t *testing.T) *os.File {
	t.Helper()
	fds := make([]int, 2)
	if err := syscall.Pipe2(fds, syscall.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Close(fds[0]) })
	if err := syscall.SetNonblock(fds[1], true); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := syscall.Write(fds[1], make([]byte, 4096))
		if errors.Is(err, syscall.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.SetNonblock(fds[1], false); err != nil {
		t.Fatal(err)
	}
	w := os.NewFile(uintptr(fds[1]), "stderr")
	t.Cleanup(func() { w.Close() })
	return w
}

// A stalled stderr drops sync's logs instead of blocking them, so Ctrl+C
// still stops it.
func TestBinarySyncStopsWithStalledStderr(t *testing.T) {
	cmd := exec.Command(binaryPath(t), "sync", "--endpoint", "127.0.0.1:1", "--dest", t.TempDir(), "--history-dir", t.TempDir())
	cmd.Dir = t.TempDir()
	cmd.Stderr = fullPipe(t)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	// Let sync start, log, and fail to connect before it is stopped.
	time.Sleep(time.Second)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		code := 0
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != 0 && code != 1 {
			t.Errorf("exit %d, want 0 or 1", code)
		}
	case <-time.After(15 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatal("sync did not stop on Ctrl+C with stderr stalled")
	}
}
