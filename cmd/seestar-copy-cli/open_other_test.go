//go:build !windows

package main

import (
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// An opener still running after openWait is a success: no warning, and it
// is not killed.
func TestLaunchStaysRunning(t *testing.T) {
	old := openWait
	openWait = 100 * time.Millisecond
	t.Cleanup(func() { openWait = old })
	hold, release, err := os.Pipe() // the opener runs until release closes
	if err != nil {
		t.Fatal(err)
	}
	defer release.Close()
	said, say, err := os.Pipe() // the opener writes to say once released
	if err != nil {
		t.Fatal(err)
	}
	defer said.Close()
	cmd := exec.Command("/bin/sh", "-c", "read x <&3; echo alive >&4; exit 1")
	cmd.ExtraFiles = []*os.File{hold, say}
	var logs lockedBuffer
	reaped := launch(cmd, slog.New(slog.NewTextHandler(&logs, nil)))
	hold.Close()
	say.Close()
	time.Sleep(3 * openWait)
	select {
	case <-reaped:
		t.Fatal("the opener ended before it was released")
	default:
	}
	if s := logs.next(); s != "" {
		t.Errorf("logged for a running opener:\n%s", s)
	}
	release.Close()
	if b, _ := io.ReadAll(said); string(b) != "alive\n" {
		t.Errorf("the opener said %q, want alive: it was killed", b)
	}
	<-reaped
	if s := logs.next(); s != "" {
		t.Errorf("logged for an exit after openWait:\n%s", s)
	}
}

// An opener that cannot start, or exits nonzero at once, is a warning; one
// that exits zero is not.
func TestLaunchFails(t *testing.T) {
	for _, tc := range []struct {
		cmd  *exec.Cmd
		want string
	}{
		{exec.Command("/nonexistent/opener"), `level=WARN msg="could not open a browser" error="fork/exec /nonexistent/opener: no such file or directory"`},
		{exec.Command("/bin/sh", "-c", "exit 3"), `level=WARN msg="could not open a browser" error="exit status 3"`},
		{exec.Command("/bin/sh", "-c", "exit 0"), ""},
	} {
		var logs lockedBuffer
		<-launch(tc.cmd, slog.New(slog.NewTextHandler(&logs, nil)))
		s := logs.next()
		if tc.want == "" && s != "" || !strings.Contains(s, tc.want) || strings.Count(s, "\n") > 1 {
			t.Errorf("%q: logged %q, want %q", tc.cmd.Args, s, tc.want)
		}
	}
}
