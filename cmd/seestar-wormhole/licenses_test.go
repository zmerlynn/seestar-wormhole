package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Exercise a real executable: Go test binaries do not carry the application's
// dependency build metadata. Run outside the source tree to prove embedding.
func TestLicensesBinary(t *testing.T) {
	check := exec.Command("go", "run", "./scripts/generate-licenses.go", "--check")
	check.Dir = "../.."
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("license catalog: %v\n%s", err, out)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "seestar-wormhole")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-mod=vendor", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(binary, "licenses")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("licenses: %v stderr=%s", err, &stderr)
	}
	text := stdout.String()
	for _, want := range []string{
		"seestar-wormhole NOTICE\n",
		"Module: github.com/pion/dtls/v3\nSPDX: MIT\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("licenses output missing %q", want)
		}
	}
	for _, path := range []string{
		"NOTICE",
		"vendor/github.com/pion/dtls/v3/LICENSE",
		"internal/edge/PION-LICENSE",
		"vendor/github.com/spf13/cobra/LICENSE.txt",
	} {
		want, err := os.ReadFile(filepath.Join("../..", path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(stdout.Bytes(), want) {
			t.Errorf("licenses output missing full text of %s", path)
		}
		if path == "NOTICE" && !bytes.HasPrefix(stdout.Bytes(), want) {
			t.Error("project NOTICE must appear first")
		}
	}
	// Compare Go's target-specific dependency set with actual module entries,
	// rather than accepting module names that merely appear in the NOTICE table.
	deps, err := exec.Command("go", "list", "-mod=vendor", "-deps", "-f", "{{if .Module}}{{if not .Module.Main}}{{.Module.Path}}{{end}}{{end}}", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	wantModules := map[string]bool{}
	for _, path := range strings.Fields(string(deps)) {
		wantModules[path] = true
	}
	for _, line := range strings.Split(text, "\n") {
		if path, ok := strings.CutPrefix(line, "Module: "); ok {
			if !wantModules[path] {
				t.Errorf("unexpected or duplicate module %s", path)
			}
			delete(wantModules, path)
		}
	}
	for path := range wantModules {
		t.Errorf("linked module %s missing from licenses output", path)
	}
}

func TestLicensesHelpAndArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"help"}} {
		var out bytes.Buffer
		if _, err := parse(args, &out); !errors.Is(err, errHelp) {
			t.Fatalf("root help: %v", err)
		}
		if !strings.HasSuffix(out.String(), "Third-party license information: seestar-wormhole licenses\n") {
			t.Fatalf("root help lacks final license notice: %s", &out)
		}
	}
	for _, args := range [][]string{{"licenses", "PRIVATE_ARGUMENT"}, {"licenses", "--PRIVATE_FLAG"}, {"licenses", "--version"}} {
		var out, logs bytes.Buffer
		err := run(context.Background(), args, &out, &logs)
		if err == nil || commandExitCode(options{command: commandLicenses}, err) != 2 || out.Len() != 0 ||
			!strings.Contains(logs.String(), "seestar-wormhole licenses --help") || strings.Contains(logs.String(), "PRIVATE_") {
			t.Fatalf("args=%v err=%v stdout=%s stderr=%s", args, err, &out, &logs)
		}
	}
	var out bytes.Buffer
	code := mainExitCodeWithBootstrap([]string{"licenses"}, &out, io.Discard,
		func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
			t.Fatal("licenses initialized signal handling")
			return nil, nil
		}, func() (io.Writer, func()) {
			t.Fatal("licenses initialized runtime logging")
			return nil, nil
		})
	if code != 0 || !strings.HasPrefix(out.String(), "seestar-wormhole NOTICE\n") {
		t.Fatalf("licenses bootstrap: code=%d stdout=%s", code, &out)
	}
}
