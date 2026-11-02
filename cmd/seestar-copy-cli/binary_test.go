package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// binary is the command built once by TestMain, with release-style link-time
// version and revision. Test binaries do not carry the command's module list,
// so licenses and exit codes are checked on a real executable.
var binary string

const (
	testVersion  = "v0.0.0-test"
	testRevision = "0123456789ab"
)

func TestMain(m *testing.M) {
	os.Exit(buildAndRun(m))
}

// errNoDevice is what dial and dialRemover return in tests that do not
// replace them, so no test connects anywhere.
var errNoDevice = errors.New("no device in tests")

func buildAndRun(m *testing.M) int {
	dial = func(context.Context, string) (remote.Session, error) { return nil, errNoDevice }
	dialRemover = func(context.Context, string) (remote.Remover, error) { return nil, errNoDevice }
	dir, err := os.MkdirTemp("", "seestar-copy-cli-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	binary = filepath.Join(dir, programName)
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-mod=vendor", "-trimpath", "-buildvcs=false",
		"-ldflags=-s -w -X main.version="+testVersion+" -X main.revision="+testRevision, "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build: %v\n%s", err, out)
		return 1
	}
	return m.Run()
}

// execute runs the built command outside the source tree.
func execute(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
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
		{[]string{"sync", "--dest", "d"}, 2},
		{[]string{"sync", "--endpoint", "h:445"}, 2},
		{[]string{"sync", "--endpoint", "h", "--dest", "d"}, 2},
		// A state folder inside the destination is refused before any connection.
		{[]string{"sync", "--endpoint", "h:445", "--dest", "d", "--state-dir", "d"}, 1},
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
	want := fmt.Sprintf("seestar-copy-cli %s (%s, %s/%s)\n", testVersion, testRevision, runtime.GOOS, runtime.GOARCH)
	if code != 0 || stdout != want || stderr != "" {
		t.Errorf("code=%d stdout=%q stderr=%q, want 0 and %q", code, stdout, stderr, want)
	}
}

func TestBinaryLicenses(t *testing.T) {
	code, text, stderr := execute(t, "licenses")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q, want 0 and no stderr", code, stderr)
	}
	read := func(path string) string {
		b, err := os.ReadFile(filepath.Join("../..", filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	notice, license := read("NOTICE"), read("LICENSE")
	if !strings.HasPrefix(text, notice) {
		t.Error("licenses output must start with the project NOTICE")
	}
	if !strings.Contains(text, "\n--- LICENSE ---\n"+license) || !strings.Contains(license, "This is free and unencumbered software released into the public domain.") {
		t.Error("licenses output lacks the full Unlicense text")
	}
	// Go's own list of the modules this target links decides what must appear.
	deps, err := exec.Command("go", "list", "-mod=vendor", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}}{{end}}{{end}}", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, path := range strings.Fields(string(deps)) {
		want[path] = true
	}
	if !want["github.com/spf13/cobra"] || !want["github.com/hirochachacha/go-smb2/v2"] || !want["golang.org/x/text"] {
		t.Fatalf("go list found %v, want cobra, go-smb2 and x/text among them", want)
	}
	krb5x := "vendor/github.com/go-krb5/x/"
	licenseFiles := map[string][]string{
		"github.com/geoffgarside/ber":          {"vendor/github.com/geoffgarside/ber/LICENSE"},
		"github.com/go-crypt/x":                {"vendor/github.com/go-crypt/x/LICENSE"},
		"github.com/go-krb5/krb5":              {"vendor/github.com/go-krb5/krb5/LICENSE"},
		"github.com/go-krb5/x":                 {krb5x + "LICENSE", krb5x + "aescts/LICENSE", krb5x + "dns/LICENSE", krb5x + "identity/LICENSE", krb5x + "rpc/LICENSE"},
		"github.com/google/uuid":               {"vendor/github.com/google/uuid/LICENSE"},
		"github.com/hirochachacha/go-smb2/v2":  {"vendor/github.com/hirochachacha/go-smb2/v2/LICENSE"},
		"github.com/inconshreveable/mousetrap": {"vendor/github.com/inconshreveable/mousetrap/LICENSE"},
		"github.com/pierrec/lz4/v4":            {"vendor/github.com/pierrec/lz4/v4/LICENSE"},
		"github.com/quic-go/quic-go":           {"vendor/github.com/quic-go/quic-go/LICENSE"},
		"github.com/spf13/cobra":               {"vendor/github.com/spf13/cobra/LICENSE.txt"},
		"github.com/spf13/pflag":               {"vendor/github.com/spf13/pflag/LICENSE"},
		"golang.org/x/crypto":                  {"vendor/golang.org/x/crypto/LICENSE"},
		"golang.org/x/net":                     {"vendor/golang.org/x/net/LICENSE"},
		"golang.org/x/sys":                     {"vendor/golang.org/x/sys/LICENSE"},
		"golang.org/x/text":                    {"vendor/golang.org/x/text/LICENSE"},
	}
	for _, line := range strings.Split(text, "\n") {
		path, ok := strings.CutPrefix(line, "Module: ")
		if !ok {
			continue
		}
		if !want[path] {
			t.Errorf("unexpected or repeated module %s", path)
		}
		delete(want, path)
		files, ok := licenseFiles[path]
		if !ok {
			t.Errorf("module %s has no license file listed in this test", path)
			continue
		}
		for _, file := range files {
			if !strings.Contains(text, "\n--- "+file+" ---\n"+read(file)) {
				t.Errorf("licenses output lacks the full text of %s", file)
			}
		}
	}
	for path := range want {
		t.Errorf("linked module %s missing from licenses output", path)
	}
}
