package licenses

import (
	"bytes"
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// Inspect each real cgo-free executable's module metadata and run the same
// notice writer used by `licenses`. Foreign executables are not executed.
func TestTargetNotices(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(goos+"/"+arch, func(t *testing.T) {
				binary := filepath.Join(t.TempDir(), "seestar-wormhole")
				cmd := exec.Command("go", "build", "-mod=vendor", "-p", "2", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", binary, "./cmd/seestar-wormhole")
				cmd.Dir = "../.."
				cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+arch)
				if b, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("build: %v\n%s", err, b)
				}
				info, err := buildinfo.ReadFile(binary)
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				if err := writeForBuild(&out, info); err != nil {
					t.Fatal(err)
				}
				text := out.String()
				for _, want := range []string{"Module: github.com/zalando/go-keyring\nSPDX: MIT AND Apache-2.0\n", "Copyright 2013 Google Inc. All Rights Reserved.", "Apache License"} {
					if !strings.Contains(text, want) {
						t.Fatalf("missing %q", want)
					}
				}
				if goos == "linux" && !strings.Contains(text, "Module: github.com/godbus/dbus/v5\nSPDX: BSD-2-Clause\n") {
					t.Fatal("Linux D-Bus notices missing")
				}
				if goos == "windows" && !strings.Contains(text, "Module: github.com/danieljoos/wincred\nSPDX: MIT\n") {
					t.Fatal("Windows Credential Manager notices missing")
				}
				t.Logf("CGO_ENABLED=0 %s/%s: build and linked-module licenses verified", goos, arch)
			})
		}
	}
}

func TestWailsSourceNotices(t *testing.T) {
	var out bytes.Buffer
	info := &debug.BuildInfo{Deps: []*debug.Module{{Path: "github.com/wailsapp/wails/v3"}}}
	if err := writeForBuild(&out, info); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"MIT AND ISC AND BSD-3-Clause", "The Winc Authors", "Copyright (c) 2013 Ato Araki", "Neither the name of @atotto.", "THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing attribution or license term %q", want)
		}
	}
}
