package session

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

// After a 60s lazy idle, demand arrives immediately and then every 5s.
// Transport loss: stop takes 523ms; the UDP outage lasts 30s. The cloud fixture
// refuses detail for 35–45s after session end, then succeeds.
// Each sequence must recover using ordinary bounded retry.
func TestReacquisition(t *testing.T) {
	for _, lazy := range []bool{true, false} {
		for _, busyFor := range []time.Duration{35 * time.Second, 45 * time.Second, 89 * time.Second} {
			for seed := uint64(1); seed <= 5; seed++ {
				t.Run(fmt.Sprintf("lazy=%v/busy=%s/seed=%d", lazy, busyFor, seed), func(t *testing.T) {
					c := DefaultConfig()
					if c.RecentReleaseWindow != 90*time.Second {
						t.Error("own-session retry window must be 90s")
					}
					if lazy {
						c.Idle = 60 * time.Second
					}
					m, err := New(c, rand.New(rand.NewPCG(seed, 9)))
					if err != nil {
						t.Fatal(err)
					}
					q := sim.New[Event](0, 512)
					timer := sim.NewTimer(q)
					schedule := func(at core.Instant, e Event) {
						t.Helper()
						if err := q.Schedule(at, e, 0); err != nil {
							t.Fatal(err)
						}
					}
					var end, stop, recovered core.Instant
					var live uint64
					refusals := 0
					var lastDial core.Instant
					schedule(0, Event{Kind: Start})
					schedule(0, Event{Kind: Client, Client: 1})
					handle := func(e Event, now core.Instant) error {
						if e.Kind == Released {
							live = 0
						}
						for _, a := range m.Step(e, now) {
							switch a.Kind {
							case Dial:
								if live != 0 {
									t.Fatal("overlapping attempts")
								}
								if refusals > 0 && now >= stop+core.Instant(c.RecentReleaseWindow) {
									t.Fatalf("refusal retry crossed horizon at %s after stop", time.Duration(now-stop))
								}
								live = a.Attempt
								if a.Attempt > 1 && now-lastDial < core.Instant(c.BackoffMin) {
									t.Fatal("retry spun")
								}
								lastDial = now
								if end != 0 && now < stop+core.Instant(busyFor) {
									refusals++
									schedule(now.Add(480*time.Millisecond), Event{Kind: Failed, Attempt: a.Attempt, Err: ErrBusy})
								} else {
									for l := Cloud; l < LayerCount; l++ {
										schedule(now.Add(time.Duration(l+1)*time.Millisecond), Event{Kind: Progress, Attempt: a.Attempt, Layer: l})
									}
								}
							case Admit:
								if end == 0 {
									if lazy {
										schedule(now.Add(time.Second), Event{Kind: ClientGone, Client: a.Client})
									} else {
										schedule(now.Add(4*time.Second), Event{Kind: Failed, Attempt: a.Attempt, Layer: Rendezvous, Err: ErrTransport})
									}
								} else if recovered == 0 {
									recovered = now
								}
							case Release:
								delay := time.Duration(0)
								owned := a.Attempt == 1
								if owned {
									end = now
									delay = 523 * time.Millisecond
									stop = now.Add(delay)
									polls := 30
									if lazy && seed == 1 {
										polls = 1
									} // one waiting caller must also recover
									for i := 0; i < polls; i++ {
										schedule(stop.Add(time.Duration(i)*5*time.Second+time.Millisecond), Event{Kind: Client, Client: uint64(i + 2)})
									}
								}
								schedule(now.Add(delay), Event{Kind: Released, Attempt: a.Attempt, ReleasedAllocation: owned})
							case ArmTimer:
								if err := timer.Arm(a.Deadline, Event{Kind: Tick}); err != nil {
									t.Fatal(err)
								}
							case CancelTimer:
								timer.Cancel()
							}
						}
						if m.Snapshot().State == Busy && busyFor < 89*time.Second {
							t.Fatalf("own-session refusal became terminal at %s after stop", time.Duration(now-stop))
						}
						return nil
					}
					if err := q.Run(core.Instant(4*time.Minute), 10000, handle); err != nil {
						t.Fatal(err)
					}
					if busyFor == 89*time.Second && recovered == 0 {
						if m.Snapshot().State != Busy || m.Snapshot().Queued != 0 {
							t.Fatalf("expired demand: %+v", m.Snapshot())
						}
						return
					}
					if refusals < 3 || recovered == 0 || recovered < stop+core.Instant(busyFor) || recovered > stop+core.Instant(busyFor+c.BackoffMax+6*time.Second) {
						t.Fatalf("refusals=%d recovery=%s after stop", refusals, time.Duration(recovered-stop))
					}
				})
			}
		}
	}
}

func TestLostSessionStartsRefusalWindow(t *testing.T) {
	m := machine(t, DefaultConfig())
	m.Step(Event{Kind: Start}, 0)
	ready(m, 0)
	m.Step(Event{Kind: Failed, Attempt: 1, Err: ErrTransport}, core.Instant(time.Second))
	// Successful local cleanup without an explicit stopped-lease marker still
	// follows our known live session. Failed/uncertain cleanup must remain blocked.
	m.Step(Event{Kind: Released, Attempt: 1}, core.Instant(2*time.Second))
	m.Step(Event{Kind: Tick}, m.Snapshot().Deadline)
	m.Step(Event{Kind: Failed, Attempt: 2, Err: ErrBusy}, core.Instant(10*time.Second))
	m.Step(Event{Kind: Released, Attempt: 2}, core.Instant(10*time.Second))
	if m.Snapshot().State != Backoff {
		t.Fatalf("lost owned session: %+v", m.Snapshot())
	}
}

func TestRetryingAdmissionCleanup(t *testing.T) {
	for _, outcome := range []string{"stop-failed", "timeout", "shutdown", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			c := DefaultConfig()
			c.Idle = time.Second
			m := machine(t, c)
			m.Step(Event{Kind: Start}, 0)
			m.Step(Event{Kind: Client, Client: 1}, 0)
			ready(m, 0)
			m.Step(Event{Kind: ClientGone, Client: 1}, 0)
			m.Step(Event{Kind: Tick}, core.Instant(time.Second))
			m.Step(Event{Kind: Released, Attempt: 1, ReleasedAllocation: true}, core.Instant(time.Second))
			m.Step(Event{Kind: Client, Client: 2}, core.Instant(2*time.Second))
			m.Step(Event{Kind: Failed, Attempt: 2, Err: ErrBusy}, core.Instant(2*time.Second))
			if m.Snapshot().Queued != 1 {
				t.Fatal("retryable refusal dropped pending admission")
			}
			now := core.Instant(3 * time.Second)
			var actions []Action
			switch outcome {
			case "stop-failed":
				actions = m.Step(Event{Kind: Released, Attempt: 2, Err: ErrStop}, now)
			case "timeout":
				actions = m.Step(Event{Kind: Tick}, m.Snapshot().Deadline)
				if has(actions, Stopped) || m.Snapshot().State != Releasing {
					t.Fatal("stop completion race terminated cleanup before join")
				}
				// The stop deadline can race a compliant worker completion. Only
				// failure to join within the additional adapter grace is terminal.
				actions = m.Step(Event{Kind: Tick}, m.Snapshot().Deadline)
			case "shutdown":
				actions = m.Step(Event{Kind: Shutdown}, now)
			case "cancel":
				actions = m.Step(Event{Kind: ClientGone, Client: 2}, now)
			}
			if m.Snapshot().Clients != 0 || m.Snapshot().Queued != 0 {
				t.Fatalf("retained dead demand: %+v", m.Snapshot())
			}
			if outcome != "cancel" && !has(actions, Reject) {
				t.Fatal("pending caller not notified")
			}
		})
	}
}
