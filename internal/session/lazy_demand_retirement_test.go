package session

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestLazyDemandRetiredByEveryDropPath(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*Machine)
		event   Event
		now     core.Instant
		want    State
		reject  error
	}{
		{"dial timeout", nil, Event{Kind: Tick}, core.Instant(30 * time.Second), Releasing, ErrTimeout},
		{"ordinary dial failure", nil, Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrTransport}, 1, Releasing, ErrTransport},
		{"non-cloud busy", nil, Event{Kind: Failed, Attempt: 1, Layer: DTLS, Err: ErrBusy}, 1, Releasing, ErrBusy},
		{"uncertain dial", nil, Event{Kind: Failed, Attempt: 1, Err: ErrUncertain}, 1, Releasing, ErrUncertain},
		{"usage budget", nil, Event{Kind: Usage, Attempt: 1, Layer: Cloud, Bytes: math.MaxInt}, 1, Releasing, ErrBudget},
		{"shutdown dial", nil, Event{Kind: Shutdown}, 1, Releasing, ErrClosed},
		{"shutdown retained cleanup", func(m *Machine) { m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0) }, Event{Kind: Shutdown}, 1, Releasing, ErrClosed},
		{"failed cleanup", func(m *Machine) { m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0) }, Event{Kind: Released, Attempt: 1, Err: ErrStop}, 1, Backoff, ErrStop},
		{"abandoned refusal cleanup", func(m *Machine) { m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0) }, Event{Kind: Released, Attempt: 1, Err: ErrReleaseAbandoned}, 1, Backoff, ErrStop},
		{"blocked backoff", func(m *Machine) {
			m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
			m.Step(Event{Kind: Released, Attempt: 1}, 0)
		}, Event{Kind: Failed, Attempt: 1, Err: ErrStop}, 1, Backoff, ErrStop},
		{"cleanup grace timeout", func(m *Machine) {
			m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
			m.Step(Event{Kind: Tick}, core.Instant(5*time.Second))
		}, Event{Kind: Tick}, core.Instant(35 * time.Second), Closed, ErrTimeout},
		{"shutdown cleanup timeout", func(m *Machine) {
			m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
			m.Step(Event{Kind: Shutdown}, 0)
		}, Event{Kind: Tick}, core.Instant(5 * time.Second), Closed, nil},
		{"attempt exhaustion", func(m *Machine) {
			m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
			m.Step(Event{Kind: Released, Attempt: 1}, 0)
			m.attempt = math.MaxUint64
		}, Event{Kind: Tick}, core.Instant(2 * time.Second), Closed, ErrClosed},
		{"deadline during dial", nil, Event{Kind: Tick}, core.Instant(90 * time.Second), Releasing, ErrBusy},
		{"deadline during release", func(m *Machine) {
			m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, core.Instant(89*time.Second))
		}, Event{Kind: Tick}, core.Instant(90 * time.Second), Releasing, ErrBusy},
		{"deadline during backoff", func(m *Machine) {
			m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
			m.Step(Event{Kind: Released, Attempt: 1}, 0)
		}, Event{Kind: Tick}, core.Instant(90 * time.Second), Dormant, ErrBusy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := lazyBusyMachine(t)
			if tt.prepare != nil {
				tt.prepare(m)
			}
			as := m.Step(tt.event, tt.now)
			found := tt.reject == nil
			for _, a := range as {
				if a.Kind == Reject && a.Client == 1 && errors.Is(a.Err, tt.reject) {
					found = true
				}
			}
			if !found || m.count != 0 || m.queued != 0 || m.lazyUntil != 0 || m.state != tt.want {
				t.Fatalf("drop: actions=%+v snapshot=%+v lazyUntil=%v; want reject=%v state=%v and retired demand", as, m.Snapshot(), m.lazyUntil, tt.reject, tt.want)
			}
		})
	}
}

func TestLazyFreshDemandAfterTimeoutBoundary(t *testing.T) {
	for _, firstBusy := range []bool{false, true} {
		for _, arrival := range []time.Duration{40 * time.Second, 90 * time.Second, 91 * time.Second} {
			t.Run(time.Duration(arrival).String()+"/"+map[bool]string{false: "initial", true: "retry"}[firstBusy], func(t *testing.T) {
				m := lazyBusyMachine(t)
				attempt := uint64(1)
				timeout := core.Instant(30 * time.Second)
				if firstBusy {
					m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
					m.Step(Event{Kind: Released, Attempt: 1}, 0)
					m.Step(Event{Kind: Tick}, m.Snapshot().Deadline)
					attempt = 2
					timeout = m.Snapshot().Deadline
				}
				as := m.Step(Event{Kind: Tick}, timeout)
				if !has(as, Reject) || !has(as, Release) {
					t.Fatalf("timeout=%+v", as)
				}
				retry, lazyRetry := m.retry, m.lazyRetry
				m.Step(Event{Kind: Released, Attempt: attempt}, timeout)
				if m.retry < retry || m.lazyRetry != lazyRetry {
					t.Fatalf("drop erased retry history: generic=%v/%v lazy=%v/%v", m.retry, retry, m.lazyRetry, lazyRetry)
				}
				m.Step(Event{Kind: Tick}, m.Snapshot().Deadline)
				now := core.Instant(arrival)
				as = m.Step(Event{Kind: Client, Client: 2}, now)
				if !has(as, Dial) || m.lazyUntil != now.Add(LazyBusyWindow) {
					t.Fatalf("fresh demand=%+v deadline=%v want %v", as, m.lazyUntil, now.Add(LazyBusyWindow))
				}
			})
		}
	}
}

func TestLazyLastClientCancellationRetiresDemand(t *testing.T) {
	for _, phase := range []string{"dial", "release", "backoff"} {
		t.Run(phase, func(t *testing.T) {
			m := lazyBusyMachine(t)
			if phase != "dial" {
				m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
			}
			if phase == "backoff" {
				m.Step(Event{Kind: Released, Attempt: 1}, 0)
			}
			m.Step(Event{Kind: ClientGone, Client: 1}, 1)
			if m.lazyUntil != 0 || m.count != 0 {
				t.Fatalf("canceled demand=%+v deadline=%v", m.Snapshot(), m.lazyUntil)
			}
		})
	}
}

func TestLazyOrdinaryFailurePreservesRetryHistoryAndCleanupOrigin(t *testing.T) {
	m := lazyBusyMachine(t)
	m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 0)
	m.Step(Event{Kind: Released, Attempt: 1}, 0)
	now := m.Snapshot().Deadline
	m.Step(Event{Kind: Tick}, now)
	history := m.lazyRetry
	as := m.Step(Event{Kind: Failed, Attempt: 2, Layer: DTLS, Err: ErrTransport}, now+1)
	if !has(as, Reject) || !has(as, Release) || m.lazyUntil != 0 || m.lazyRetry != history || m.releaseRefusal || m.refusalRetry || !m.retryPending {
		t.Fatalf("transport failure after busy: actions=%+v deadline=%v history=%v want %v releaseRefusal=%v refusalRetry=%v retryPending=%v", as, m.lazyUntil, m.lazyRetry, history, m.releaseRefusal, m.refusalRetry, m.retryPending)
	}
	// Transport recovery's existing abandonment permission is independent of a
	// prior refusal. It cannot become a blocking lazy-refusal cleanup failure.
	m.Step(Event{Kind: Released, Attempt: 2, Err: ErrReleaseAbandoned}, now+2)
	if m.blockErr != nil || m.state != Backoff {
		t.Fatalf("transport cleanup inherited refusal ownership: %+v block=%v", m.Snapshot(), m.blockErr)
	}
	m.Step(Event{Kind: Tick}, m.Snapshot().Deadline)
	as = m.Step(Event{Kind: Client, Client: 2}, core.Instant(40*time.Second))
	if !has(as, Dial) || m.lazyUntil != core.Instant(130*time.Second) {
		t.Fatalf("fresh demand after transport cleanup=%+v deadline=%v want 130s", as, m.lazyUntil)
	}
}
