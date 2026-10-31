//go:build gui

package main

import (
	"fmt"
	"io"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConsolePreservesRedirectedOutput(t *testing.T) {
	for _, id := range []uint32{windows.STD_OUTPUT_HANDLE, windows.STD_ERROR_HANDLE} {
		for _, pipe := range []bool{false, true} {
			t.Run(fmt.Sprintf("handle=%d/pipe=%t", id, pipe), func(t *testing.T) {
				previous, err := windows.GetStdHandle(id)
				if err != nil {
					t.Fatal(err)
				}
				defer windows.SetStdHandle(id, previous)
				var output, reader *os.File
				if pipe {
					reader, output, err = os.Pipe()
				} else {
					output, err = os.CreateTemp(t.TempDir(), "redirected")
					reader = output
				}
				if err != nil {
					t.Fatal(err)
				}
				defer output.Close()
				if pipe {
					defer reader.Close()
				}
				if err := windows.SetStdHandle(id, windows.Handle(output.Fd())); err != nil {
					t.Fatal(err)
				}
				handle, valid := inheritedOutput(id)
				if !valid {
					t.Fatal("valid redirection classified as missing")
				}
				// Model AttachConsole replacing the process handle. Restoring the inherited
				// stream must keep the original file/pipe and its Go file object.
				if err := windows.SetStdHandle(id, windows.InvalidHandle); err != nil {
					t.Fatal(err)
				}
				original := output
				restoreConsoleOutput(id, handle, valid, &output)
				current, err := windows.GetStdHandle(id)
				if err != nil || current != handle || output != original {
					t.Fatalf("redirection changed: %v", err)
				}
				if _, err := output.WriteString("redirected\n"); err != nil {
					t.Fatal(err)
				}
				if !pipe {
					if _, err := reader.Seek(0, 0); err != nil {
						t.Fatal(err)
					}
				}
				data := make([]byte, len("redirected\n"))
				if _, err := io.ReadFull(reader, data); err != nil || string(data) != "redirected\n" {
					t.Fatalf("output=%q, err=%v", data, err)
				}
			})
		}
	}
}
