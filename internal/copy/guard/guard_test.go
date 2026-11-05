package guard

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// Run passes f's error through, and turns a panic into a logged error.
func TestRun(t *testing.T) {
	var b bytes.Buffer
	log := slog.New(slog.NewTextHandler(&b, nil))
	want := errors.New("refused")
	if err := Run(log, "copying", func() error { return want }); err != want {
		t.Errorf("Run = %v, want f's error", err)
	}
	if b.Len() != 0 {
		t.Errorf("logged without a panic: %s", b.String())
	}
	err := Run(log, "copying", func() error { panic("boom") })
	if err == nil || err.Error() != "internal error while copying: boom" {
		t.Errorf("Run after a panic = %v", err)
	}
	if s := b.String(); !strings.Contains(s, "internal error recovered") || !strings.Contains(s, "where=copying") || !strings.Contains(s, "panic=boom") || !strings.Contains(s, "guard_test.go") {
		t.Errorf("log after a panic, want where, panic and stack: %s", s)
	}
}

// Recover, deferred at a goroutine's start, ends the goroutine instead of
// the process, and the goroutine's own deferred cleanup still runs.
func TestRecover(t *testing.T) {
	var b bytes.Buffer
	log := slog.New(slog.NewTextHandler(&b, nil))
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer Recover(log, "deleting")
		panic(errors.New("boom"))
	}()
	<-done
	if s := b.String(); !strings.Contains(s, "where=deleting") || !strings.Contains(s, "panic=boom") {
		t.Errorf("log after a panic: %s", s)
	}
	Recover(log, "unused") // not deferred and no panic: a no-op
}
