//go:build !windows

package opener

import (
	"context"
	"testing"
)

// macOS uses its open by absolute path; every other system, xdg-open.
func TestProgram(t *testing.T) {
	for goos, want := range map[string]string{"darwin": "/usr/bin/open", "linux": "xdg-open", "freebsd": "xdg-open"} {
		if got := program(goos); got != want {
			t.Errorf("program(%q) = %q, want %q", goos, got, want)
		}
	}
}

// Command passes target as the opener's one argument and leaves its stdio
// unset.
func TestCommand(t *testing.T) {
	target := "file:///a b.html?x=1&y=2"
	cmd := Command(context.Background(), target)
	if len(cmd.Args) != 2 || cmd.Args[1] != target {
		t.Errorf("args = %q, want the opener and %q", cmd.Args, target)
	}
	if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		t.Error("stdio is set, want the null device")
	}
}
