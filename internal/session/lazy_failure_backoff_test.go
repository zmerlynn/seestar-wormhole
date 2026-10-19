package session

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestLazyFailuresRespectBackoff(t *testing.T) {
	c := DefaultConfig()
	c.Idle = time.Second
	m := machine(t, c)
	m.Step(Event{Kind: Start}, 0)
	m.Step(Event{Kind: Client, Client: 1}, 0)
	m.Step(Event{Kind: Failed, Attempt: 1}, 1)
	m.Step(Event{Kind: Released, Attempt: 1}, 2)
	as := m.Step(Event{Kind: Client, Client: 2}, 3)
	if has(as, Dial) {
		t.Fatalf("redial at 3ns after failure at 1ns: want no Dial before >=%v; snapshot=%+v", c.BackoffMin, m.Snapshot())
	}
}

func TestLazyRetryDemandAndExponentialHistory(t *testing.T) {
	for _, arrival := range []int{-1, 0, 1} {
		c := DefaultConfig()
		c.Idle = time.Second
		m := machine(t, c)
		m.Step(Event{Kind: Start}, 0)
		var now core.Instant
		for attempt := uint64(1); attempt <= 5; attempt++ {
			m.Step(Event{Kind: Client, Client: attempt}, now)
			m.Step(Event{Kind: Failed, Attempt: attempt}, now+1)
			m.Step(Event{Kind: Released, Attempt: attempt}, now+2)
			deadline := now + 2 + core.Instant(c.BackoffMin*time.Duration(1<<(attempt-1)))
			if got := m.Snapshot().Deadline; got != deadline {
				t.Fatalf("attempt=%d deadline=%d want=%d", attempt, got, deadline)
			}
			if m.Snapshot().Attempt != attempt {
				t.Fatal("empty demand bypassed backoff")
			}
			m.Step(Event{Kind: Client, Client: 100 + attempt}, now+5)
			m.Step(Event{Kind: ClientGone, Client: 100 + attempt}, now+6)
			if arrival >= 0 {
				m.Step(Event{Kind: Tick}, deadline)
				if m.Snapshot().Attempt != attempt {
					t.Fatal("empty lazy retry allocated")
				}
			}
			now = deadline + core.Instant(arrival)
			as := m.Step(Event{Kind: Client, Client: attempt + 1}, now)
			if arrival < 0 {
				if has(as, Dial) {
					t.Fatal("early demand bypassed backoff")
				}
				m.Step(Event{Kind: Tick}, deadline)
				now = deadline
			}
			if m.Snapshot().Attempt != attempt+1 {
				t.Fatalf("demand never dialed: %+v", m.Snapshot())
			}
		}
	}
}
