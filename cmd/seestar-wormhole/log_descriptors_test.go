//go:build linux

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestProxyLogHelper(t *testing.T) {
	if os.Getenv("PROXY_LOG_HELPER") == "1" {
		if os.Getenv("PROXY_LOG_FULL") == "1" {
			for {
				_, err := unix.SendmsgN(2, make([]byte, 4096), nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
				if err == unix.EAGAIN {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		os.Args = []string{"seestar-wormhole", "proxy", "--idle", "password-path-sentinel"}
		main()
	}
}

// Exercise stderr handling with pipes, files, sockets, and a full socket buffer.
func TestProxyStderrDescriptors(t *testing.T) {
	for _, sink := range []string{"pipe", "file", "socket", "full socket"} {
		t.Run(sink, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProxyLogHelper$")
			cmd.Env = append(os.Environ(), "PROXY_LOG_HELPER=1")
			var buf bytes.Buffer
			var reader, writer *os.File
			switch sink {
			case "pipe":
				cmd.Stderr = &buf
			case "file":
				var err error
				writer, err = os.CreateTemp(t.TempDir(), "stderr")
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
				cmd.Stderr = writer
			case "socket", "full socket":
				if sink == "full socket" {
					cmd.Env = append(cmd.Env, "PROXY_LOG_FULL=1")
				}
				pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
				if err != nil {
					t.Fatal(err)
				}
				reader = os.NewFile(uintptr(pair[0]), "reader")
				writer = os.NewFile(uintptr(pair[1]), "writer")
				defer reader.Close()
				defer writer.Close()
				cmd.Stderr = writer
			}
			if err := cmd.Run(); err == nil {
				t.Fatal("invalid arguments succeeded")
			}
			if ctx.Err() != nil {
				t.Fatal("stderr blocked process exit")
			}
			data := buf.Bytes()
			if sink == "socket" || sink == "full socket" {
				writer.Close()
				var err error
				data, err = io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
			} else if sink == "file" {
				var err error
				data, err = os.ReadFile(writer.Name())
				if err != nil {
					t.Fatal(err)
				}
			}
			if sink != "full socket" && !bytes.Contains(data, []byte(`"reason":"invalid_option"`)) {
				t.Fatalf("stderr = %q; want sanitized fatal diagnostic", data)
			}
			if strings.Contains(string(data), "password-path-sentinel") {
				t.Fatal("argument leaked")
			}
		})
	}
}
