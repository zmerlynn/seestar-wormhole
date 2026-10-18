package session

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestBlockingFailureCannotHideClientDeadline(t *testing.T) {
	m := lazyBusyMachine(t)
	m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
	m.Step(Event{Kind: Released, Attempt: 1}, 0)
	m.Step(Event{Kind: Tick}, m.Snapshot().Deadline)
	m.Step(Event{Kind: Failed, Attempt: 2, Layer: Cloud, Err: ErrBusy}, core.Instant(89*time.Second))
	m.Step(Event{Kind: Failed, Attempt: 2, Layer: Rendezvous, Err: ErrStop}, core.Instant(89*time.Second)+1)
	as := m.Step(Event{Kind: Tick}, core.Instant(90*time.Second))
	if m.Snapshot().Queued != 0 {
		t.Fatalf("late stop failure hid client deadline: actions=%+v state=%+v; want no queued demand at 90s", as, m.Snapshot())
	}
}

// Expiring demand cannot release ownership or erase an earlier blocking failure,
// even when the final cleanup completion itself reports success.

func TestLazyBusyDeadlinePreservesBlockingCleanup(t *testing.T) {
	for _, failure := range []error{ErrStop, ErrUncertain, errors.Join(ErrBusy, ErrReleaseAbandoned)} {
		for _, cleanup := range []error{nil, ErrStop, ErrReleaseAbandoned} {
			t.Run(failure.Error()+"/"+fmt.Sprint(cleanup), func(t *testing.T) {
				m := lazyBusyMachine(t)
				m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
				m.Step(Event{Kind: Released, Attempt: 1}, 0)
				m.Step(Event{Kind: Tick}, m.Snapshot().Deadline)
				m.Step(Event{Kind: Failed, Attempt: 2, Layer: Cloud, Err: ErrBusy}, core.Instant(89*time.Second))
				m.Step(Event{Kind: Failed, Attempt: 2, Layer: Rendezvous, Err: failure}, core.Instant(89*time.Second)+1)
				if m.Snapshot().Deadline != core.Instant(90*time.Second) {
					t.Fatalf("blocking failure changed client deadline: %+v", m.Snapshot())
				}
				as := m.Step(Event{Kind: Tick}, core.Instant(90*time.Second))
				if !has(as, Reject) || has(as, Dial) || has(as, Stopped) || has(as, Release) || m.Snapshot().State != Releasing || m.Snapshot().Queued != 0 {
					t.Fatalf("expiry lost worker ownership: actions=%+v state=%+v", as, m.Snapshot())
				}
				as = m.Step(Event{Kind: Client, Client: 2}, core.Instant(91*time.Second))
				if !has(as, Reject) || has(as, Dial) {
					t.Fatalf("blocking cleanup queued replacement: %+v", as)
				}
				as = m.Step(Event{Kind: Released, Attempt: 2, Err: cleanup}, core.Instant(93*time.Second))
				if has(as, Dial) || m.Snapshot().State != Busy {
					t.Fatalf("joined cleanup forgot blocking failure: actions=%+v state=%+v", as, m.Snapshot())
				}
				as = m.Step(Event{Kind: Client, Client: 3}, core.Instant(94*time.Second))
				if !has(as, Reject) || has(as, Dial) {
					t.Fatalf("replacement authorized after blocking failure: %+v", as)
				}
				as = m.Step(Event{Kind: Shutdown}, core.Instant(95*time.Second))
				for _, a := range as {
					if a.Kind == Stopped && (errors.Is(a.Err, ErrStop) || errors.Is(a.Err, ErrUncertain)) {
						return
					}
				}
				t.Fatalf("shutdown lost blocking failure: %+v", as)
			})
		}
	}
}

func TestLazyBusyResidualFailureKeepsClientBudget(t *testing.T) {
	for _, failure := range []error{ErrTransport, ErrTimeout, ErrBudget} {
		t.Run(failure.Error(), func(t *testing.T) {
			m := lazyBusyMachine(t)
			m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
			m.Step(Event{Kind: Released, Attempt: 1}, 0)
			m.Step(Event{Kind: Tick}, m.Snapshot().Deadline)
			m.Step(Event{Kind: Failed, Attempt: 2, Layer: Cloud, Err: ErrBusy}, core.Instant(89*time.Second))
			m.Step(Event{Kind: Failed, Attempt: 2, Layer: Rendezvous, Err: failure}, core.Instant(89*time.Second)+1)
			as := m.Step(Event{Kind: Tick}, core.Instant(90*time.Second))
			if !has(as, Reject) || has(as, Dial) || m.Snapshot().Queued != 0 || m.Snapshot().State != Releasing {
				t.Fatalf("residual failure erased client budget: actions=%+v state=%+v", as, m.Snapshot())
			}
		})
	}
}
