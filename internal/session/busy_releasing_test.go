package session

import (
	"errors"
	"fmt"
	"testing"
)

func TestClientDuringTerminalCleanup(t *testing.T) {
	for _, failure := range []error{ErrBusy, ErrUncertain, ErrStop} {
		for _, command := range []Kind{Tick, Shutdown} {
			t.Run(fmt.Sprintf("%v/%d", failure, command), func(t *testing.T) {
				m := machine(t, DefaultConfig())
				m.Step(Event{Kind: Start}, 0)
				m.Step(Event{Kind: Client, Client: 1}, 0)
				assertReject := func(actions []Action, id uint64, want error) {
					t.Helper()
					for _, a := range actions {
						if a.Kind == Reject && a.Client == id && errors.Is(a.Err, want) {
							return
						}
					}
					t.Fatalf("client %d actions=%+v, want Reject %v", id, actions, want)
				}
				assertReject(m.Step(Event{Kind: Failed, Attempt: 1, Err: failure}, 1), 1, failure)
				want := failure
				if command == Shutdown {
					want = ErrClosed
				}
				m.Step(Event{Kind: command}, 2)
				assertReject(m.Step(Event{Kind: Client, Client: 2}, 3), 2, want)
				m.Step(Event{Kind: Released, Attempt: 1}, 4)
				assertReject(m.Step(Event{Kind: Client, Client: 3}, 5), 3, want)
			})
		}
	}
}
