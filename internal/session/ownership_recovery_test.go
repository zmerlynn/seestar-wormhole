package session

import (
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestOwnershipRecovery(t *testing.T) {
	for _, ready := range []bool{false, true} {
		for _, cause := range []error{ErrStop, ErrUncertain} {
			c := DefaultConfig()
			c.RetryBusy = true
			m := machine(t, c)
			m.Step(Event{Kind: Start}, 0)
			if ready {
				for l := Cloud; l < LayerCount; l++ {
					m.Step(Event{Kind: Progress, Attempt: 1, Layer: l}, 0)
				}
			}
			m.Step(Event{Kind: Failed, Attempt: 1, Err: cause}, 1)
			as := m.Step(Event{Kind: Released, Attempt: 1, Err: cause}, 2)
			end := core.Instant(time.Minute) + 2
			if has(as, Dial) || m.Snapshot().State != Backoff || m.Snapshot().Deadline != end {
				t.Fatalf("ready=%v cause=%v: want fixed recovery delay: %+v", ready, cause, m.Snapshot())
			}
			if as := m.Step(Event{Kind: Client, Client: 1}, 3); !has(as, Reject) {
				t.Fatalf("eager recovery held client: %+v", as)
			}
			if as := m.Step(Event{Kind: Tick}, end-1); has(as, Dial) {
				t.Fatal("early dial")
			}
			if as := m.Step(Event{Kind: Tick}, end); !has(as, Dial) {
				t.Fatalf("no fresh dial: %+v", as)
			}
			if as := m.Step(Event{Kind: Client, Client: 2}, end); !has(as, Reject) {
				t.Fatalf("fresh recovery dial held eager client: %+v", as)
			}
			// Fresh refusals retain ambient retry beyond the old own-session window.
			for i := 0; i < 10; i++ {
				a := m.Snapshot().Attempt
				m.Step(Event{Kind: Failed, Attempt: a, Layer: Cloud, Err: ErrBusy}, end)
				m.Step(Event{Kind: Released, Attempt: a}, end)
				end = m.Snapshot().Deadline
				if m.Snapshot().State != Backoff {
					t.Fatal("fresh busy became sticky")
				}
				if as := m.Step(Event{Kind: Tick}, end); !has(as, Dial) {
					t.Fatalf("busy retry stopped: %+v", as)
				}
			}
			for l := Cloud; l < LayerCount; l++ {
				m.Step(Event{Kind: Progress, Attempt: m.Snapshot().Attempt, Layer: l}, end)
			}
			if m.Snapshot().State != Ready {
				t.Fatal("did not recover readiness")
			}
		}
	}
}

func TestOwnershipRecoveryLazyDemand(t *testing.T) {
	for _, arrival := range []time.Duration{10 * time.Second, 2 * time.Minute} {
		for _, cancel := range []bool{false, true} {
			c := DefaultConfig()
			c.Idle = time.Hour
			c.LazyBusyWindow = LazyBusyWindow
			m := machine(t, c)
			m.Step(Event{Kind: Start}, 0)
			m.Step(Event{Kind: Client, Client: 1}, 0)
			m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrUncertain}, 1)
			m.Step(Event{Kind: Released, Attempt: 1}, 2)
			end := core.Instant(time.Minute) + 2
			if arrival > time.Minute {
				if as := m.Step(Event{Kind: Tick}, end); has(as, Dial) || m.Snapshot().State != Dormant {
					t.Fatal("lazy recovery dialed without demand")
				}
			}
			as := m.Step(Event{Kind: Client, Client: 2}, core.Instant(arrival))
			if arrival < time.Minute && (has(as, Dial) || has(as, Reject)) {
				t.Fatalf("demand bypassed delay: %+v", as)
			}
			if cancel && arrival < time.Minute {
				m.Step(Event{Kind: ClientGone, Client: 2}, core.Instant(arrival)+1)
				if as := m.Step(Event{Kind: Tick}, end); has(as, Dial) || m.Snapshot().State != Dormant {
					t.Fatal("canceled lazy demand dialed")
				}
				continue
			}
			now := core.Instant(arrival)
			if arrival < time.Minute {
				now = end
				as = m.Step(Event{Kind: Tick}, now)
			}
			if !has(as, Dial) || m.lazyUntil != now.Add(LazyBusyWindow) {
				t.Fatalf("fresh demand missing dial/deadline: %+v %+v", as, m.Snapshot())
			}
			// A busy reply still expires from the first fresh dial, without extending
			// the budget for later demand or the earlier ownership recovery delay.
			m.Step(Event{Kind: Failed, Attempt: 2, Layer: Cloud, Err: ErrBusy}, now)
			m.Step(Event{Kind: Released, Attempt: 2}, now)
			m.Step(Event{Kind: Client, Client: 3}, now+1)
			as = m.Step(Event{Kind: Tick}, now.Add(LazyBusyWindow))
			if !has(as, Reject) || has(as, Dial) || m.Snapshot().Queued != 0 {
				t.Fatalf("lazy budget extended: %+v %+v", as, m.Snapshot())
			}
		}
	}
}

func TestOwnershipRecoveryShutdown(t *testing.T) {
	for _, duringDial := range []bool{false, true} {
		m := machine(t, DefaultConfig())
		m.Step(Event{Kind: Start}, 0)
		ready(m, 0)
		m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrTransport}, 1)
		m.Step(Event{Kind: Released, Attempt: 1, Err: ErrStop}, 2)
		now := m.Snapshot().Deadline
		if duringDial {
			m.Step(Event{Kind: Tick}, now)
		}
		as := m.Step(Event{Kind: Shutdown}, now)
		if duringDial {
			if !has(as, Release) || has(as, Stopped) {
				t.Fatalf("fresh dial not released: %+v", as)
			}
			as = m.Step(Event{Kind: Released, Attempt: 2}, now+1)
		}
		if !has(as, Stopped) || m.Snapshot().State != Closed {
			t.Fatalf("shutdown not complete: %+v", as)
		}
		if has(m.Step(Event{Kind: Tick}, now.Add(time.Hour)), Dial) {
			t.Fatal("dial after shutdown")
		}
	}
}

func TestOwnershipRecoveryReturnsToTransportBackoff(t *testing.T) {
	for _, cleanup := range []error{nil, ErrReleaseAbandoned} {
		m := machine(t, DefaultConfig())
		m.Step(Event{Kind: Start}, 0)
		ready(m, 0)
		m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrTransport}, 1)
		m.Step(Event{Kind: Released, Attempt: 1, Err: ErrStop}, 2)
		now := m.Snapshot().Deadline
		m.Step(Event{Kind: Tick}, now)
		m.Step(Event{Kind: Failed, Attempt: 2, Err: ErrTransport}, now)
		m.Step(Event{Kind: Released, Attempt: 2, Err: cleanup}, now)
		delay := time.Duration(m.Snapshot().Deadline - now)
		if m.Snapshot().State != Backoff || delay < DefaultConfig().BackoffMin || delay > DefaultConfig().BackoffMax {
			t.Fatalf("transport recovery used ownership delay: %+v", m.Snapshot())
		}
	}
}
