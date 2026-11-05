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

// Catch returns f's panic with its stack and logs nothing; Report then
// logs it as Run would. A nil Panic reports nothing.
func TestCatch(t *testing.T) {
	if p := Catch(func() {}); p != nil {
		t.Errorf("Catch without a panic = %v, want nil", p)
	}
	var b bytes.Buffer
	log := slog.New(slog.NewTextHandler(&b, nil))
	p := Catch(func() { panic("boom") })
	if p == nil || p.Value != "boom" || !strings.Contains(string(p.Stack), "guard_test.go") {
		t.Fatalf("Catch after a panic = %v, want boom and a stack", p)
	}
	if b.Len() != 0 {
		t.Errorf("Catch logged: %s", b.String())
	}
	if err := p.Report(log, "aborting"); err == nil || err.Error() != "internal error while aborting: boom" {
		t.Errorf("Report = %v", err)
	}
	if s := b.String(); !strings.Contains(s, "where=aborting") || !strings.Contains(s, "panic=boom") || !strings.Contains(s, "guard_test.go") {
		t.Errorf("log after Report, want where, panic and stack: %s", s)
	}
	var none *Panic
	if err := none.Report(log, "unused"); err != nil {
		t.Errorf("nil Report = %v", err)
	}
}
