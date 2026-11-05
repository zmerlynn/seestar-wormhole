package app

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/engine"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

func panicDial(context.Context, string) (remote.Session, error) { panic("boom") }

// A panic in the copy loop ends the loop with an error instead of the
// process, and copying can start again.
func TestCopyPanicEndsTheLoop(t *testing.T) {
	e := newCopyEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("abc")})
	e.c.dial = panicDial
	if err := e.c.start(e.set); err != nil {
		t.Fatal(err)
	}
	e.waitOff()
	v := e.c.view(time.Now())
	if v.On || !strings.Contains(v.Title, "internal error while copying: boom") {
		t.Errorf("after a panic: %+v", v)
	}
	if err := e.c.start(e.set); err != nil {
		t.Errorf("start after a panic: %v", err)
	}
}

// A panic in a preview fails the preview.
func TestPreviewPanicFails(t *testing.T) {
	e := newPreviewEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("a")})
	e.p.dial = panicDial
	if v := e.run(); v.State != pvFailed || !strings.Contains(v.Title, "internal error while previewing: boom") {
		t.Errorf("after a panic: %+v", v)
	}
}

// A panic in a free-space check fails the check.
func TestFreePanicFails(t *testing.T) {
	e := newFreeEnv(t, "MyWorks/a.jpg")
	e.f.dial = panicDial
	if err := os.MkdirAll(e.set.Folder, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := e.f.find(e.set); err != nil {
		t.Fatal(err)
	}
	e.f.mu.Lock()
	done := e.f.done
	e.f.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the check did not end")
		}
	}
	e.f.mu.Lock()
	st := e.f.st
	e.f.mu.Unlock()
	if st.state != frCheckFail || st.err == nil || st.err.Error() != "internal error while freeing up space: boom" {
		t.Errorf("after a panic: state %v, err %v", st.state, st.err)
	}
}

// panicHandler panics on the record whose message is msg.
type panicHandler struct {
	slog.Handler
	msg string
}

func (h panicHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == h.msg {
		panic("log panic")
	}
	return h.Handler.Handle(ctx, r)
}

// quits fails the test unless quit returns within a while.
func quits(t *testing.T, quit func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		quit()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Quit waits on a loop a panic ended")
	}
}

// A panic outside the loop's own recovery, here while it logs how it
// ended, still ends copying: it is off, and Quit returns.
func TestCopyFinishPanic(t *testing.T) {
	e := newCopyEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("abc")})
	e.c.log = slog.New(panicHandler{e.c.log.Handler(), "copying stopped"})
	e.c.dial = panicDial
	if err := e.c.start(e.set); err != nil {
		t.Fatal(err)
	}
	e.waitOff()
	if e.c.on() {
		t.Error("copying is still on")
	}
	quits(t, e.c.quit)
}

// A panic while a free-space run records its outcome still ends the run:
// it is no longer running, and Quit returns.
func TestFreeEndPanic(t *testing.T) {
	e := newFreeEnv(t, "MyWorks/a.jpg")
	e.f.dial = panicDial
	ended := make(chan struct{})
	if err := e.f.run(freeStatus{state: frChecking, set: e.set}, engine.DeleteOptions{DryRun: true}, nil, 0, func(engine.DeleteResult, error, bool) {
		close(ended)
		panic("end panic")
	}); err != nil {
		t.Fatal(err)
	}
	<-ended
	deadline := time.Now().Add(10 * time.Second)
	for e.f.running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if e.f.running() {
		t.Error("Free up space is still running")
	}
	quits(t, e.f.quit)
}
