//go:build guitest && linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLaunchPreservesDiagnosticsWhenStderrReopens(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fixture")
	// Match the CLI's independent stderr open. On a regular file its offset is
	// zero; the final error overwrites the diagnostics instead of following them.
	script := "#!/bin/sh\nprintf 'DOM diagnostics: evaluated\n'\nprintf 'final process error\n' > /proc/self/fd/2\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	_, done, err := launch(t.Context(), bin, filepath.Join(dir, "out"), dir, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "out", "process.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DOM diagnostics: evaluated", "final process error"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing %q: %s", want, b)
		}
	}
}

func TestLaunchBoundsInheritedOutputHandles(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fixture")
	script := "#!/bin/sh\nsleep 6 &\necho $! > \"$SEESTAR_GUITEST_OUT/child\"\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	defer func() {
		b, _ := os.ReadFile(filepath.Join(out, "child"))
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			if child, err := os.FindProcess(pid); err == nil {
				child.Kill()
			}
		}
	}()
	start := time.Now()
	_, done, err := launch(t.Context(), bin, out, dir, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal("output drain changed clean process result", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("inherited output handles escaped the drain bound")
	}
	b, err := os.ReadFile(filepath.Join(out, "process.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "Warning: inherited process output remained open after exit") {
		t.Fatal("missing truncated-output warning")
	}
}
