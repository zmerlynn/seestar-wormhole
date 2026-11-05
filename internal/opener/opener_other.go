//go:build !windows

package opener

import (
	"context"
	"os/exec"
	"runtime"
)

// Command returns the system's opener for target: open on macOS, xdg-open
// elsewhere. Its stdin, stdout and stderr are unset, so they are the null
// device and a text-mode browser cannot take over a terminal. ctx kills it
// as exec.CommandContext does.
func Command(ctx context.Context, target string) *exec.Cmd {
	return exec.CommandContext(ctx, program(runtime.GOOS), target)
}

func program(goos string) string {
	if goos == "darwin" {
		return "/usr/bin/open"
	}
	return "xdg-open"
}
