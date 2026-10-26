package session

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestPermanentFailureBlocksDemandThroughCleanup(t *testing.T) {
	for _, terminal := range []error{ErrStop, ErrUncertain, errors.Join(ErrStop, ErrBusy), errors.Join(ErrUncertain, ErrBusy)} {
		for _, pending := range []bool{false, true} {
			m := machine(t, DefaultConfig())
			m.Step(Event{Kind: Start}, 0)
			if pending {
				m.Step(Event{Kind: Failed, Attempt: 1}, 1)
			}
			m.Step(Event{Kind: Failed, Attempt: 1, Err: terminal, Permanent: true}, 2)
			m.Step(Event{Kind: Client, Client: 2}, 3)
			m.Step(Event{Kind: Released, Attempt: 1}, 5)
			m.Step(Event{Kind: Tick}, core.Instant(time.Hour))
			if m.Snapshot().Attempt != 1 || m.Snapshot().State != Busy {
				t.Fatalf("uncertain replacement: %+v", m.Snapshot())
			}
			if as := m.Step(Event{Kind: Client, Client: 3}, core.Instant(time.Hour)); has(as, Dial) || !has(as, Reject) {
				t.Fatal("completed uncertain cleanup admitted demand")
			}
		}
	}
}
