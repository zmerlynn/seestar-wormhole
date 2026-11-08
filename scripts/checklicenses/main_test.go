package main

import (
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	info := &debug.BuildInfo{Deps: []*debug.Module{{Path: "example.com/a"}, {Path: "example.com/b"}}}
	for _, tc := range []struct {
		name, text, reject string
	}{
		{"exact", "NOTICE\n\nModule: example.com/a\nSPDX: MIT\n\nModule: example.com/b\nSPDX: MIT\n", ""},
		{"missing", "NOTICE\n\nModule: example.com/a\nSPDX: MIT\n", "omits linked module example.com/b"},
		{"duplicate", "\nModule: example.com/a\n\nModule: example.com/a\n\nModule: example.com/b\n", "lists example.com/a 2 times"},
		{"not linked", "\nModule: example.com/a\n\nModule: example.com/b\n\nModule: example.com/c\n", "lists example.com/c, which is not linked"},
		{"mention only", "NOTICE names example.com/a and example.com/b\n", "omits linked module example.com/a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := compare(info, tc.text)
			if tc.reject == "" && err != nil || tc.reject != "" && (err == nil || !strings.Contains(err.Error(), tc.reject)) {
				t.Fatalf("compare=%v, want %q", err, tc.reject)
			}
		})
	}
}

// The shipped CLI passes when built the way a release builds it.
func TestShippedPrograms(t *testing.T) {
	t.Chdir("../..")
	paths, err := build(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || strings.TrimSuffix(filepath.Base(paths[0]), ".exe") != "seestar-wormhole-cli" {
		t.Fatalf("built %v, want only seestar-wormhole-cli", paths)
	}
	if err := run(paths); err != nil {
		t.Fatal(err)
	}
}

// A Go executable without its execute permission, as artifact transfer
// leaves one, is refused by name rather than failing to start.
func TestNotExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no execute permission bits")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "downloaded")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{path}); err == nil || !strings.Contains(err.Error(), path+" is not executable (mode -rw-r--r--)") {
		t.Fatalf("run(0644 executable) = %v, want it refused as not executable", err)
	}
}
