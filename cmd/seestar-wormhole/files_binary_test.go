package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

const (
	testVersion  = "v0.0.0-test"
	testRevision = "0123456789ab"
)

// builtBinary builds the program once, with release-style link-time version
// and revision. Its file commands have no test seams, so every case here is
// refused by validation or points at a closed loopback port.
var builtBinary = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "seestar-wormhole-test")
	if err != nil {
		return "", err
	}
	binaryDir = dir
	binary := filepath.Join(dir, programName)
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-mod=vendor", "-trimpath", "-buildvcs=false",
		"-ldflags=-s -w -X main.version="+testVersion+" -X main.revision="+testRevision, "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build: %v\n%s", err, out)
	}
	return binary, nil
})

func binaryPath(t *testing.T) string {
	t.Helper()
	if guiEnabled {
		t.Skip("the GUI program is built with its own tags")
	}
	binary, err := builtBinary()
	if err != nil {
		t.Fatal(err)
	}
	return binary
}

// execute runs the built program outside the source tree.
func execute(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(binaryPath(t), args...)
	cmd.Dir = t.TempDir()
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	err := cmd.Run()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode(), out.String(), errs.String()
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0, out.String(), errs.String()
}

// The exit codes of the file commands, on the built program.
func TestBinaryExitCodes(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{nil, 0},
		{[]string{"sync", "--help"}, 0},
		{[]string{"help", "sync"}, 0},
		{[]string{"help", "not-a-command"}, 2},
		{[]string{"--bogus"}, 2},
		{[]string{"sync", "--version"}, 2},
		{[]string{"sync", "--dest", "d"}, 2},
		{[]string{"sync", "--endpoint", "h:445"}, 2},
		{[]string{"sync", "--endpoint", "h", "--dest", "d"}, 2},
		// A history folder inside the destination is refused before any connection.
		{[]string{"sync", "--endpoint", "h:445", "--dest", "d", "--state-dir", "d"}, 1},
		// find's stdout is its listing, so a failed find leaves it empty.
		{[]string{"find", "--endpoint", "127.0.0.1:1"}, 1},
	} {
		code, stdout, stderr := execute(t, tc.args...)
		if code != tc.code {
			t.Errorf("%q: exit %d, want %d\nstdout=%s\nstderr=%s", tc.args, code, tc.code, stdout, stderr)
		}
		if (code == 0) != (stderr == "") || (code == 0) == (stdout == "") {
			t.Errorf("%q: stdout=%q stderr=%q, want output on stdout only on success", tc.args, stdout, stderr)
		}
	}
}

func TestBinaryVersion(t *testing.T) {
	code, stdout, stderr := execute(t, "--version")
	want := fmt.Sprintf("%s %s (%s, %s/%s)\n", programName, testVersion, testRevision, runtime.GOOS, runtime.GOARCH)
	if code != 0 || stdout != want || stderr != "" {
		t.Errorf("code=%d stdout=%q stderr=%q, want 0 and %q", code, stdout, stderr, want)
	}
}

// binaryDir holds the built program, if a test built it.
var binaryDir string

func TestMain(m *testing.M) {
	code := m.Run()
	if binaryDir != "" {
		os.RemoveAll(binaryDir)
	}
	os.Exit(code)
}
