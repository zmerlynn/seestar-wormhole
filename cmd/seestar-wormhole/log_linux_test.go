//go:build linux

package main

import (
	"errors"
	"golang.org/x/sys/unix"
	"testing"
	"time"
)

func TestFullLogPipeCannotBlock(t *testing.T) {
	fds := make([]int, 2)
	if err := unix.Pipe2(fds, unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	b := make([]byte, 4096)
	for {
		_, err := unix.Write(fds[1], b)
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() { newLogger("json", rawLog(fds[1])).Info("shutdown"); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("full log pipe blocked")
	}
}
