package tracerecord

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"testing"
	"time"
)

func TestReplayDetectsChangedActionsAndMissingSteps(t *testing.T) {
	s := new(memorySink)
	w := New(s, 4096, func() time.Time { return time.Unix(0, 0) })
	m, _ := rdt.New(rdt.DefaultConfig())
	e := rdt.Event{Kind: rdt.Start}
	w.Record("rdt", 1, 0, e, m.Step(e, 0))
	w.Close()
	rr, err := Read(bytes.NewReader(s.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	fresh := func() func(rdt.Event, core.Instant) []rdt.Action { m, _ := rdt.New(rdt.DefaultConfig()); return m.Step }
	hydrate := func(r Record) (rdt.Event, error) { return rdt.Event{Kind: rdt.EventKind(r.Input.Kind)}, nil }
	if err := Replay(rr, "rdt", 1, hydrate, fresh()); err != nil {
		t.Fatal(err)
	}
	rr[0].Outputs[0].Type++
	if err := Replay(rr, "rdt", 1, hydrate, fresh()); err == nil {
		t.Fatal("changed action accepted")
	}
	rr[0].Outputs[0].Type--
	rr[0].Dropped = 1
	called := false
	step := func(e rdt.Event, now core.Instant) []rdt.Action { called = true; return nil }
	if err := Replay(rr, "rdt", 1, hydrate, step); err == nil || called {
		t.Fatal("lossy trace executed")
	}
	rr[0].Dropped = 0
	if err := Replay(rr[:len(rr)-1], "rdt", 1, hydrate, step); err == nil || called {
		t.Fatal("incomplete trace executed")
	}
	if err := Replay(rr, "tunnel", 2, hydrate, step); err == nil {
		t.Fatal("absent instance accepted")
	}
}

func FuzzRead(f *testing.F) {
	f.Add([]byte(`{"version":1,"record":"step","sequence":1}` + "\n"))
	f.Add([]byte(`{"version":1,"record":"end","sequence":2,"dropped":1}` + "\n"))
	f.Add([]byte(`null`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, b []byte) {
		rr, err := Read(bytes.NewReader(b))
		if err == nil {
			for _, r := range rr {
				if r.Version != 1 {
					t.Fatal("accepted incompatible version")
				}
			}
		}
	})
}
