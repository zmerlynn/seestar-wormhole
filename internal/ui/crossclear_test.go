package ui

import (
	"slices"
	"testing"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// copyCrossCleared is the Copy operations every pass clears, whatever
// its own outcome.
var copyCrossCleared = []string{diagnostic.OpStart, diagnostic.OpOpen, diagnostic.OpOtherTelescope}

// watermark is the newest sequence number the trail took for source's op.
func (e *trailEnv) watermark(source, op string) uint64 {
	l := e.s.controller.log
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastSeq[opKey{source, op}]
}

// nextPass runs the next pass, optionally with its walk failing: it
// starts copying, or with copying on starts the next pass now. It returns
// the pass's outcome once copying has stopped.
func (e *trailEnv) nextPass(fails bool) copyapp.Outcome {
	e.t.Helper()
	if fails {
		e.fail(stepWalk, errSynthetic)
	}
	n := len(e.outcomes(diagnostic.SourceCopy, diagnostic.OpPass)) + 1
	action := "start"
	if e.s.copy.Busy() {
		action = "check"
	}
	e.mustPost("/copy/api/copy", `{"action":"`+action+`"}`, 200)
	o := e.reported(diagnostic.SourceCopy, diagnostic.OpPass, n)
	e.stopCopying()
	e.fail(stepWalk, nil)
	if o.Failed() != fails {
		e.t.Fatalf("pass = %+v, want failed %v", o, fails)
	}
	return o
}

// A pass, clean or failed, ends a Copy start, open or other telescope
// failure: the trail writes its recovery line, drops the failure and
// takes the pass's number as that operation's watermark. Each failure
// is made through the Mount's own handlers.
func TestPassClearsCopyFailures(t *testing.T) {
	for _, tc := range []struct {
		op   string
		fail func(e *trailEnv)
	}{
		// A Start that succeeds clears its own failure, so the pass
		// here is the next one of copying already on, which a Start
		// refused as busy leaves running.
		{diagnostic.OpStart, func(e *trailEnv) {
			e.ready()
			e.pass(1)
			e.mustPost("/copy/api/copy", `{"action":"start"}`, 409)
			e.reported(diagnostic.SourceCopy, diagnostic.OpStart, 2)
		}},
		{diagnostic.OpOpen, func(e *trailEnv) {
			e.ready()
			release := e.lockFolder()
			e.mustPost("/copy/api/copy", `{"action":"start"}`, 200)
			e.reported(diagnostic.SourceCopy, diagnostic.OpOpen, 1)
			e.until("copying to end", func() bool { return !e.s.copy.Busy() })
			release()
		}},
		{diagnostic.OpOtherTelescope, func(e *trailEnv) {
			a := e.account()
			if code, body := e.start(testSettings()); code != 200 {
				e.t.Fatalf("Connect to A = %d %s", code, body)
			}
			a.markReady("synthetic-device-a")
			e.settings()
			e.pass(1)
			a.stop()
			if code, body := e.start(testSettings()); code != 200 {
				e.t.Fatalf("Connect to B = %d %s", code, body)
			}
			a.markReady("synthetic-device-b")
			e.until("copying to end", func() bool {
				e.post("/copy/api/copy", `{"action":"check"}`)
				return !e.s.copy.Busy()
			})
			// The first pass's success is other_telescope's first outcome.
			if o := e.reported(diagnostic.SourceCopy, diagnostic.OpOtherTelescope, 2); !o.Failed() {
				e.t.Fatalf("other telescope = %+v", o)
			}
			a.stop()
			if code, body := e.start(testSettings()); code != 200 {
				e.t.Fatalf("Connect to A again = %d %s", code, body)
			}
			a.markReady("synthetic-device-a")
		}},
	} {
		for _, passFails := range []bool{false, true} {
			name := tc.op + "/clean pass"
			if passFails {
				name = tc.op + "/failed pass"
			}
			t.Run(name, func(t *testing.T) {
				e := newTrailEnv(t)
				tc.fail(e)
				failedCode := diagnostic.Code(diagnostic.SourceCopy, tc.op, true)
				if got := e.failing(); !slices.Equal(got, []string{"copy/" + tc.op}) {
					t.Fatalf("failing before the pass = %v", got)
				}
				pass := e.nextPass(passFails)
				// A pass's outcomes reach the trail in one send, the pass
				// first, so these lines come in this order.
				want := []string{failedCode, diagnostic.Code(diagnostic.SourceCopy, tc.op, false)}
				wantFailing := []string(nil)
				if passFails {
					want = slices.Insert(want, 1, "copy_pass_failed")
					wantFailing = []string{"copy/pass"}
				}
				if got := e.fileCodes(); !slices.Equal(got, want) {
					t.Errorf("codes = %v, want %v", got, want)
				}
				if got := e.failing(); !slices.Equal(got, wantFailing) {
					t.Errorf("failing after the pass = %v, want %v", got, wantFailing)
				}
				for _, op := range copyCrossCleared {
					if w := e.watermark(diagnostic.SourceCopy, op); w != pass.Seq {
						t.Errorf("copy/%s watermark = %d, want the pass's %d", op, w, pass.Seq)
					}
				}
			})
		}
	}
}

// A pass moves the start, open and other telescope watermarks even when
// none of them has failed, so an older failure of one held until after
// the pass can't open it: a refused Start, numbered before the pass but
// reaching the trail after it, writes no line and leaves nothing failing.
func TestPassFencesOlderCopyFailures(t *testing.T) {
	for _, passFails := range []bool{false, true} {
		name := "clean pass"
		if passFails {
			name = "failed pass"
		}
		t.Run(name, func(t *testing.T) {
			e := newTrailEnv(t)
			e.settings()
			held, old, release := e.holdOutcome(diagnostic.SourceCopy, diagnostic.OpStart, failed)
			refused := make(chan int, 1)
			go func() {
				code, _ := e.post("/copy/api/copy", `{"action":"start"}`)
				refused <- code
			}()
			<-held
			e.lan("192.0.2.10")
			pass := e.nextPass(passFails)
			if pass.Seq <= old.Seq {
				t.Fatalf("pass %+v is not newer than the refused start %+v", pass, *old)
			}
			for _, op := range copyCrossCleared {
				if w := e.watermark(diagnostic.SourceCopy, op); w != pass.Seq {
					t.Errorf("copy/%s watermark before the release = %d, want the pass's %d", op, w, pass.Seq)
				}
			}
			release()
			if code := <-refused; code != 409 {
				t.Fatalf("refused start = %d, want 409", code)
			}
			e.until("the held start to reach the trail", func() bool {
				return slices.ContainsFunc(e.outcomes(diagnostic.SourceCopy, diagnostic.OpStart), func(o copyapp.Outcome) bool { return o.Seq == old.Seq })
			})
			want := []string(nil)
			wantFailing := []string(nil)
			if passFails {
				want, wantFailing = []string{"copy_pass_failed"}, []string{"copy/pass"}
			}
			if got := e.fileCodes(); !slices.Equal(got, want) {
				t.Errorf("codes = %v, want %v", got, want)
			}
			if got := e.failing(); !slices.Equal(got, wantFailing) {
				t.Errorf("failing = %v, want %v", got, wantFailing)
			}
			if w := e.watermark(diagnostic.SourceCopy, diagnostic.OpStart); w != pass.Seq {
				t.Errorf("copy/start watermark = %d, want the pass's %d", w, pass.Seq)
			}
		})
	}
}
