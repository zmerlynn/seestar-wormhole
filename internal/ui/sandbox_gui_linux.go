//go:build gui

package ui

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

const sandboxRequirement = "WebKit sandbox unavailable. Install bubblewrap and allow unprivileged user namespaces. See docs/troubleshooting.md (Linux native window)."

func checkWindowSandbox() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return probeWindowSandbox(ctx, func(ctx context.Context, name string, args ...string) error {
		return exec.CommandContext(ctx, name, args...).Run()
	})
}

// Check the sandbox before WebKit enters native code, where a denied uid map
// terminates the process with SIGTRAP instead of returning a startup error.
func probeWindowSandbox(ctx context.Context, run func(context.Context, string, ...string) error) error {
	if err := run(ctx, "bwrap", "--ro-bind", "/", "/", "--unshare-user", "--unshare-pid", "--proc", "/proc", "--dev", "/dev", "--", "/bin/true"); err != nil {
		return errors.New(sandboxRequirement)
	}
	return nil
}
