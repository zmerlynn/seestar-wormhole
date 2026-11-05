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
	if err == nil || err.Error() != "internal error while copying" {
		t.Errorf("Run after a panic = %v", err)
	}
	checkRecord(t, &b, "copying")
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
	checkRecord(t, &b, "deleting")
	Recover(log, "unused") // not deferred and no panic: a no-op
}

// Catch returns f's panic and logs nothing; Report then
// logs it as Run would. A nil Panic reports nothing.
func TestCatch(t *testing.T) {
	if p := Catch(func() {}); p != nil {
		t.Errorf("Catch without a panic = %v, want nil", p)
	}
	var b bytes.Buffer
	log := slog.New(slog.NewTextHandler(&b, nil))
	p := Catch(func() { panic("boom") })
	if p == nil || p.Value != "boom" {
		t.Fatalf("Catch after a panic = %v, want boom", p)
	}
	if b.Len() != 0 {
		t.Errorf("Catch logged: %s", b.String())
	}
	if err := p.Report(log, "aborting"); err == nil || err.Error() != "internal error while aborting" {
		t.Errorf("Report = %v", err)
	}
	checkRecord(t, &b, "aborting")
	var none *Panic
	if err := none.Report(log, "unused"); err != nil {
		t.Errorf("nil Report = %v", err)
	}
}

// checkRecord checks that b holds one record of a panic recovered where,
// naming the site and neither the panic value nor its stack.
func checkRecord(t *testing.T, b *bytes.Buffer, where string) {
	t.Helper()
	want := `level=ERROR msg="internal error recovered" where=` + where + "\n"
	if s := b.String(); !strings.HasSuffix(s, want) || strings.Count(s, "\n") != 1 ||
		strings.Contains(s, "boom") || strings.Contains(s, "guard_test.go") {
		t.Errorf("log %q, want one record ending %q", s, want)
	}
}
