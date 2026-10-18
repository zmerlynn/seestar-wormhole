//go:build linux

package main

import (
	"bytes"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"strings"
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

func TestUnixLogLossReceipt(t *testing.T) {
	for _, mode := range []string{"closed", "broken_pipe", "full_pipe", "broken_socket"} {
		t.Run(mode, func(t *testing.T) {
			var output io.Writer = rawLog(-1)
			if mode == "broken_socket" {
				pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
				if err != nil {
					t.Fatal(err)
				}
				unix.Close(pair[0])
				defer unix.Close(pair[1])
				output = socketLog(pair[1])
			} else if mode != "closed" {
				fd := make([]int, 2)
				if err := unix.Pipe2(fd, unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
					t.Fatal(err)
				}
				defer unix.Close(fd[1])
				output = rawLog(fd[1])
				if mode == "broken_pipe" {
					unix.Close(fd[0])
				} else {
					defer unix.Close(fd[0])
					for {
						_, err := unix.Write(fd[1], make([]byte, 4096))
						if errors.Is(err, unix.EAGAIN) {
							break
						}
						if err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			w := &logLossWriter{output: output}
			if n, err := w.Write([]byte("lost\n")); n != 0 || err == nil {
				t.Fatalf("lost write n=%d err=%v", n, err)
			}
			var logs bytes.Buffer
			w.output = &logs
			w.Write([]byte("recovered\n"))
			if w.Err() == nil || w.Dropped() != 1 || !strings.Contains(logs.String(), `"rule":"log_sink_loss"`) || !strings.Contains(logs.String(), `"level":"WARN"`) {
				t.Fatalf("missing loss receipt: %s", &logs)
			}
		})
	}
}
