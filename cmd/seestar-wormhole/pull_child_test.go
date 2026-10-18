package main

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func pullChildCommand(t *testing.T, test string) *exec.Cmd {
	t.Helper()
	// The watchdog covers fixture writes and durable checkpoint seeding as well
	// as the scenario under test. Allow slow storage under the full race gate;
	// leave time for the parent to report the child's timeout stack and assertions.
	timeout := 2 * time.Minute
	if deadline, ok := t.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline)*9/10)
	}
	if timeout <= 0 {
		t.Fatal("no time remains to run pull child before the parent test deadline")
	}
	t.Logf("%s child timeout=%s (includes fixture writes and checkpoint fsync)", test, timeout)
	return exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+test+"$", "-test.timeout="+timeout.String())
}
