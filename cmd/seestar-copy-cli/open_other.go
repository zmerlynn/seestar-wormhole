//go:build !windows

package main

import (
	"log/slog"
	"os/exec"
	"runtime"
	"time"
)

// openWait is how long an opener is watched for a failed exit. One still
// running after it has launched the browser, and is left to run.
var openWait = 3 * time.Second

// openURL runs the system's opener on url, without stdin, stdout or stderr,
// so a text-mode browser cannot take over the terminal. It returns once the
// opener has started.
func openURL(url string, log *slog.Logger) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "/usr/bin/open"
	}
	launch(exec.Command(name, url), log)
}

// launch starts cmd and reaps it in the background. A failed start, or a
// failed exit within openWait, is a warning; cmd is never killed. The
// channel closes once cmd has been reaped.
func launch(cmd *exec.Cmd, log *slog.Logger) <-chan struct{} {
	reaped := make(chan struct{})
	start := time.Now()
	if err := cmd.Start(); err != nil {
		warnOpen(log, err)
		close(reaped)
		return reaped
	}
	go func() {
		defer close(reaped)
		if err := cmd.Wait(); err != nil && time.Since(start) < openWait {
			warnOpen(log, err)
		}
	}()
	return reaped
}
