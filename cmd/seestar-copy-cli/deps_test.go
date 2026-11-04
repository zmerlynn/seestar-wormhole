package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoWindowLibrary checks that nothing the CLI imports reaches Wails,
// even when built with the app's gui tag: the CLI stays free of the window
// and its native libraries.
func TestNoWindowLibrary(t *testing.T) {
	for _, tags := range []string{"", "gui,production"} {
		out, err := exec.Command("go", "list", "-mod=vendor", "-deps", "-tags="+tags, ".").CombinedOutput()
		if err != nil {
			t.Fatalf("go list -tags=%q: %v\n%s", tags, err, out)
		}
		for _, p := range strings.Fields(string(out)) {
			if strings.HasPrefix(p, "github.com/wailsapp/") {
				t.Errorf("with tags %q the CLI imports %s", tags, p)
			}
		}
	}
}
