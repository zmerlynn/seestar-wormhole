package session

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// An SMB retry can arrive before the failed attempt's cleanup completes.
// It must wait for that cleanup and backoff without spending another retry.
func TestPullRetryDuringTransportCleanup(t *testing.T) {
	for _, tc := range []struct {
		acquired bool
		budget   time.Duration
	}{{false, 90 * time.Second}, {true, 90 * time.Second}, {true, 0}} {
		for _, stopErr := range []error{nil, ErrStop} {
			t.Run(fmt.Sprintf("acquired=%t/budget=%s/failed-stop=%t", tc.acquired, tc.budget, stopErr != nil), func(t *testing.T) {
				c := DefaultConfig()
				c.Idle = time.Hour
				c.SingleBusyWindow = true
				c.LazyBusyWindow = tc.budget
				m := machine(t, c)
				m.Step(Event{Kind: Start}, 0)
				m.Step(Event{Kind: Client, Client: 1}, 0)
				failedAt := core.Instant(20 * time.Second)
				if tc.acquired {
					ready(m, 0)
					failedAt = core.Instant(10 * time.Minute)
				}
				m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrTransport}, failedAt)
				m.Step(Event{Kind: ClientGone, Client: 1}, failedAt)
				as := m.Step(Event{Kind: Client, Client: 2}, failedAt+1)
				if has(as, Reject) || has(as, Dial) || m.Snapshot().Queued != 1 {
					t.Fatalf("retry must wait for cleanup: actions=%+v snapshot=%+v", as, m.Snapshot())
				}
				as = m.Step(Event{Kind: Released, Attempt: 1, Err: stopErr}, failedAt+2)
				if stopErr != nil {
					if !has(as, Reject) || !errors.Is(as[0].Err, ErrStop) || has(as, Dial) {
						t.Fatalf("failed cleanup must block retry: %+v", as)
					}
					return
				}
				if has(as, Dial) || m.Snapshot().State != Backoff {
					t.Fatalf("retry skipped transport backoff: %+v %+v", as, m.Snapshot())
				}
				as = m.Step(Event{Kind: Tick}, m.Snapshot().Deadline)
				if !has(as, Dial) || m.Snapshot().Attempt != 2 {
					t.Fatalf("retry never dialed: %+v %+v", as, m.Snapshot())
				}
				if m.acquisitionUntil != core.Instant(tc.budget) {
					t.Fatalf("retry renewed acquisition deadline: %v", m.acquisitionUntil)
				}
			})
		}
	}
}

func TestPullQueuedRetryRetiresBeforeCleanup(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancel), func(t *testing.T) {
			c := DefaultConfig()
			c.Idle = time.Hour
			c.SingleBusyWindow = true
			c.LazyBusyWindow = 90 * time.Second
			m := machine(t, c)
			m.Step(Event{Kind: Start}, 0)
			m.Step(Event{Kind: Client, Client: 1}, 0)
			m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrTransport}, core.Instant(89*time.Second))
			m.Step(Event{Kind: Client, Client: 2}, core.Instant(89*time.Second))
			if cancel {
				m.Step(Event{Kind: ClientGone, Client: 2}, core.Instant(89*time.Second))
			} else {
				as := m.Step(Event{Kind: Tick}, core.Instant(90*time.Second))
				if !has(as, Reject) || !errors.Is(as[0].Err, ErrBusy) {
					t.Fatalf("queued retry missed original deadline: %+v", as)
				}
			}
			as := m.Step(Event{Kind: Released, Attempt: 1}, core.Instant(90*time.Second))
			if has(as, Dial) || m.Snapshot().Clients != 0 || m.Snapshot().Attempt != 1 {
				t.Fatalf("retired demand triggered replacement: %+v %+v", as, m.Snapshot())
			}
		})
	}
}
