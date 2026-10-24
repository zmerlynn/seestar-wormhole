//go:build darwin

package main

import (
	"context"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestDarwinLogHelper(t *testing.T) {
	if os.Getenv("DARWIN_LOG_HELPER") == "1" {
		os.Args = []string{"seestar-wormhole", "proxy", "--idle", "invalid"}
		main()
	}
}

// Darwin's fallback necessarily shares flags with inherited stderr. Even a
// fatal argument error must restore them, and a full pipe must not stall exit.
func TestDarwinStderrRestoresFlags(t *testing.T) {
	fds := make([]int, 2)
	if err := unix.Pipe(fds); err != nil {
		t.Fatal(err)
	}
	read := os.NewFile(uintptr(fds[0]), "read")
	write := os.NewFile(uintptr(fds[1]), "write")
	defer read.Close()
	defer write.Close()
	original, err := unix.FcntlInt(write.Fd(), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(fds[1], true); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := unix.Write(fds[1], make([]byte, 4096))
		if err == unix.EAGAIN {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := unix.FcntlInt(write.Fd(), unix.F_SETFL, original); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDarwinLogHelper$")
	cmd.Env = append(os.Environ(), "DARWIN_LOG_HELPER=1")
	cmd.Stderr = write
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err == nil {
		t.Fatal("invalid args exited successfully")
	}
	if ctx.Err() != nil {
		t.Fatal("full stderr stalled exit")
	}
	after, err := unix.FcntlInt(write.Fd(), unix.F_GETFL, 0)
	// Filling the pipe sets Darwin's FWASWRITTEN (0x10000) bookkeeping bit.
	// Only O_NONBLOCK belongs to logWriterFD; the kernel's write history may differ.
	if err != nil || after&unix.O_NONBLOCK != original&unix.O_NONBLOCK {
		t.Fatalf("stderr O_NONBLOCK %#x, want %#x, err %v", after&unix.O_NONBLOCK, original&unix.O_NONBLOCK, err)
	}
}
