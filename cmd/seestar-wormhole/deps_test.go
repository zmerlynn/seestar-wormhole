//go:build !gui

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoWindowLibrary checks that nothing seestar-wormhole-cli imports
// reaches Wails: the CLI, and with it the file commands, stays free of the
// window and its native libraries. The app's gui build links Wails by
// design.
func TestNoWindowLibrary(t *testing.T) {
	out, err := exec.Command("go", "list", "-mod=vendor", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, p := range strings.Fields(string(out)) {
		if strings.HasPrefix(p, "github.com/wailsapp/") {
			t.Errorf("the CLI imports %s", p)
		}
	}
}
