package session

import (
	"fmt"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestRefusalRetryCannotCrossHorizon(t *testing.T) {
	for _, mode := range []string{"timer", "delayed-timer", "later-lazy-demand", "exact-deadline"} {
		t.Run(mode, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Idle = time.Second
			m := machine(t, cfg)
			m.Step(Event{Kind: Start}, 0)
			m.Step(Event{Kind: Client, Client: 1}, 0)
			ready(m, 0)
			m.Step(Event{Kind: ClientGone, Client: 1}, 0)
			m.Step(Event{Kind: Tick}, core.Instant(time.Second))
			m.Step(Event{Kind: Released, Attempt: 1, ReleasedAllocation: true}, core.Instant(time.Second))
			end := core.Instant(time.Second + cfg.RecentReleaseWindow)
			m.Step(Event{Kind: Client, Client: 2}, end-core.Instant(500*time.Millisecond))
			m.Step(Event{Kind: Failed, Attempt: 2, Err: ErrBusy}, end-core.Instant(500*time.Millisecond))
			m.Step(Event{Kind: Released, Attempt: 2}, end-core.Instant(500*time.Millisecond))
			at := m.Snapshot().Deadline
			client := uint64(2)
			var actions []Action
			switch mode {
			case "exact-deadline":
				actions = m.Step(Event{Kind: Tick}, end)
			case "timer":
				at += core.Instant(250 * time.Millisecond)
				actions = m.Step(Event{Kind: Tick}, at)
			case "delayed-timer":
				at += core.Instant(time.Hour)
				actions = m.Step(Event{Kind: Tick}, at)
			case "later-lazy-demand":
				m.Step(Event{Kind: ClientGone, Client: 2}, end-core.Instant(200*time.Millisecond))
				m.Step(Event{Kind: Tick}, at)
				at += core.Instant(time.Hour)
				client = 3
				actions = m.Step(Event{Kind: Client, Client: client}, at)
			}
			for _, a := range actions {
				if a.Kind == Dial {
					t.Fatalf("automatic Dial attempt=%d at %s, own-session retry horizon ended at %s; want no Dial and sticky busy", a.Attempt, time.Duration(at), time.Duration(end))
				}
			}
			if m.Snapshot().State != Busy || m.Snapshot().Queued != 0 {
				t.Fatalf("expired retry: %+v", m.Snapshot())
			}
			rejected := false
			for _, a := range actions {
				if a.Kind == Reject && a.Client == client && a.Err == ErrBusy {
					rejected = true
				}
			}
			if !rejected {
				t.Fatalf("missing busy rejection: %+v", actions)
			}
			if a := m.Step(Event{Kind: Client, Client: 4}, at+1); has(a, Dial) || len(a) != 1 || a[0].Err != ErrBusy {
				t.Fatalf("busy not sticky: %+v", a)
			}

		})
	}
}

func TestExactRefusalBoundary(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Idle = time.Second
	m := machine(t, cfg)
	m.Step(Event{Kind: Start}, 0)
	m.Step(Event{Kind: Client, Client: 1}, 0)
	ready(m, 0)
	m.Step(Event{Kind: ClientGone, Client: 1}, 0)
	m.Step(Event{Kind: Tick}, core.Instant(time.Second))
	m.Step(Event{Kind: Released, Attempt: 1, ReleasedAllocation: true}, core.Instant(time.Second))
	end := core.Instant(time.Second + cfg.RecentReleaseWindow)
	m.Step(Event{Kind: Client, Client: 2}, end-1)
	m.Step(Event{Kind: Failed, Attempt: 2, Err: ErrBusy}, end)
	m.Step(Event{Kind: Released, Attempt: 2}, end)
	if m.Snapshot().State != Busy || m.Snapshot().Queued != 0 {
		t.Fatalf("at exclusive horizon: %+v; want Busy without queued demand", m.Snapshot())
	}
}

func TestExpiredWindowWithoutRefusalAllowsDemand(t *testing.T) {
	for _, transport := range []bool{false, true} {
		t.Run(fmt.Sprint(transport), func(t *testing.T) {
			c := DefaultConfig()
			c.Idle = time.Second
			m := machine(t, c)
			m.Step(Event{Kind: Start}, 0)
			m.Step(Event{Kind: Client, Client: 1}, 0)
			ready(m, 0)
			if transport {
				m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrTransport}, core.Instant(time.Second))
			} else {
				m.Step(Event{Kind: ClientGone, Client: 1}, 0)
				m.Step(Event{Kind: Tick}, core.Instant(time.Second))
			}
			m.Step(Event{Kind: Released, Attempt: 1, ReleasedAllocation: true}, core.Instant(time.Second))
			m.Step(Event{Kind: Tick}, core.Instant(time.Hour))
			if a := m.Step(Event{Kind: Client, Client: 2}, core.Instant(time.Hour)); !has(a, Dial) {
				t.Fatalf("fresh demand blocked: %+v", a)
			}
		})
	}
}
