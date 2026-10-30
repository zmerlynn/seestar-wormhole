//go:build linux && gui && cgo

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLinuxGUIBinaryNotices(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "seestar-wormhole")
	if out, err := exec.Command("go", "build", "-mod=vendor", "-tags", "gui,production", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(binary, "licenses")
	cmd.Dir = t.TempDir()
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("licenses: %v; stderr=%s", err, &stderr)
	}
	notice, err := os.ReadFile("../../NOTICE")
	if err != nil {
		t.Fatal(err)
	}
	_, native, ok := bytes.Cut(notice, []byte("\nLinux native GUI\n"))
	if !ok || !bytes.Contains(out.Bytes(), native) {
		t.Fatal("Linux GUI executable omits its native notice or full LGPL text")
	}
	if !bytes.Contains(out.Bytes(), []byte("--- internal/licenses/notices/github.com/wailsapp/wails/v3/fyne-systray.txt ---")) {
		t.Fatal("Linux GUI executable omits the systray origin license")
	}
}
