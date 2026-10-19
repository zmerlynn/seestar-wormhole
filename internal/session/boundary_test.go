package session

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"math"
	"testing"
	"time"
)

func TestShutdownCannotBeUndone(t *testing.T) {
	for _, k := range []Kind{Start, Client, ClientGone, Tick, Shutdown} {
		t.Run(string(rune('A'+k)), func(t *testing.T) {
			m := machine(t, DefaultConfig())
			m.Step(Event{Kind: Start}, 0)
			m.Step(Event{Kind: Shutdown}, 1)
			m.Step(Event{Kind: k}, 2)
			as := m.Step(Event{Kind: Released, Attempt: 1}, 3)
			if m.state != Closed || has(as, Dial) {
				t.Fatalf("event %v undid shutdown: %+v, %+v", k, m.Snapshot(), as)
			}
		})
	}
}
func TestExhaustedClockRejectsQueuedClient(t *testing.T) {
	c := DefaultConfig()
	c.Idle = time.Second
	m := machine(t, c)
	m.Step(Event{Kind: Start}, 0)
	as := m.Step(Event{Kind: Client, Client: 1}, core.Instant(math.MaxInt64))
	if !has(as, Reject) || m.Snapshot().Clients != 0 {
		t.Fatalf("clock exhaustion stranded client: %+v %+v", as, m.Snapshot())
	}
}

func TestShutdownReportsFailedStop(t *testing.T) {
	m := machine(t, DefaultConfig())
	m.Step(Event{Kind: Start}, 0)
	m.Step(Event{Kind: Shutdown}, 1)
	as := m.Step(Event{Kind: Released, Attempt: 1, Err: ErrStop}, 2)
	for _, a := range as {
		if a.Kind == Stopped {
			if a.Err != ErrStop {
				t.Fatalf("shutdown error = %v, want %v", a.Err, ErrStop)
			}
			return
		}
	}
	t.Fatal("missing stopped notification")
}

func TestOwnerCanCancelClientBeforeAdmissionStamp(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		m := machine(t, DefaultConfig())
		m.Step(Event{Kind: Start}, 0)
		m.Step(Event{Kind: Client, Client: 42}, 0)
		if admitted {
			ready(m, 0)
		}
		// An owner command uses a lifetime-unique client ID; a queued socket cannot
		// know the attempt stamp before the edge sends its first Admit notification.
		m.Step(Event{Kind: ClientGone, Client: 42}, 1)
		if m.Snapshot().Clients != 0 {
			t.Fatalf("unstamped client cancellation retained client (admitted=%v)", admitted)
		}
	}
}
