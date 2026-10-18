package session_test

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord/tracetest"
	"math/rand/v2"
	"testing"
)

func TestRecordedSimulatorReplay(t *testing.T) {
	capture := tracetest.New(t)
	cfg := session.TraceScenario(t, func(e session.Event, aa []session.Action, now core.Instant) { capture.Step("session", 1, e, aa, now) })
	rr := capture.Fixture("testdata/trace-recovery.jsonl")
	m, err := session.New(cfg, rand.New(rand.NewPCG(42, 43)))
	if err != nil {
		t.Fatal(err)
	}
	hydrate := func(r tracerecord.Record) (session.Event, error) {
		s := r.Input
		return session.Event{Kind: session.Kind(s.Kind), Attempt: s.Attempt, Client: s.Request, Layer: session.Layer(s.Type), Bytes: s.Length, Count: s.Count, ReleasedAllocation: s.Flag}, nil
	}
	if err := tracerecord.Replay(rr, "session", 1, hydrate, m.Step); err != nil {
		t.Fatal(err)
	}
}
