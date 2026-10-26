package session

import (
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestAmbientRefusalsRecoverBeyondOwnReleaseWindow(t *testing.T) {
	for _, jitter := range []fixedRNG{0, ^fixedRNG(0)} {
		cfg := DefaultConfig()
		cfg.RetryBusy = true
		cfg.BackoffMax = time.Minute
		m, err := New(cfg, jitter)
		if err != nil {
			t.Fatal(err)
		}
		m.Step(Event{Kind: Start}, 0)
		ready(m, 0)
		m.Step(Event{Kind: Failed, Attempt: 1, Layer: Rendezvous, Err: ErrTransport}, 1)
		m.Step(Event{Kind: Released, Attempt: 1, ReleasedAllocation: true}, 2)
		now := m.Snapshot().Deadline
		for i := 0; i < 1000; i++ {
			if !has(m.Step(Event{Kind: Tick}, now), Dial) {
				t.Fatalf("retry %d at %v did not dial: %+v", i, now, m.Snapshot())
			}
			m.Step(Event{Kind: Failed, Attempt: m.attempt, Layer: Cloud, Err: ErrBusy}, now)
			m.Step(Event{Kind: Released, Attempt: m.attempt}, now)
			next := m.Snapshot().Deadline
			gap := time.Duration(next - now)
			if gap < cfg.BackoffMin || gap > time.Minute || (i > 10 && gap < 30*time.Second) {
				t.Fatalf("retry %d gap=%v, want capped equal jitter", i, gap)
			}
			now = next
		}
		if time.Duration(now) < 8*time.Hour {
			t.Fatalf("test did not cross an overnight outage: %v", now)
		}
		if !has(m.Step(Event{Kind: Tick}, now), Dial) {
			t.Fatal("final recovery did not dial")
		}
		ready(m, now)
		if !has(m.Step(Event{Kind: Client, Client: 1}, now), Admit) {
			t.Fatal("recovery did not admit client")
		}
	}
}

func TestAmbientPolicyStillBlocksPermanentRejection(t *testing.T) {
	for _, cause := range []error{ErrStop, ErrUncertain} {
		cfg := DefaultConfig()
		cfg.RetryBusy = true
		m := machine(t, cfg)
		m.Step(Event{Kind: Start}, 0)
		m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: cause, Permanent: true}, 1)
		m.Step(Event{Kind: Released, Attempt: 1}, 2)
		if has(m.Step(Event{Kind: Tick}, core.Instant(24*time.Hour)), Dial) || m.Snapshot().State != Busy {
			t.Fatalf("unsafe retry for %v: %+v", cause, m.Snapshot())
		}
	}
}

func TestLazyClientRetriesTerminalCloudBusy(t *testing.T) {
	m := lazyBusyMachine(t)
	m.Step(Event{Kind: Failed, Attempt: 1, Layer: Rendezvous, Err: ErrTransport}, 1)
	m.Step(Event{Kind: Released, Attempt: 1}, 2)
	// A current-generation refusal can arrive after transport cleanup has settled.
	m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 3)
	if m.Snapshot().State != Busy {
		t.Fatal("did not reproduce terminal cloud busy")
	}
	if has(m.Step(Event{Kind: Client, Client: 2}, 4), Dial) {
		t.Fatal("fresh demand bypassed minimum interval")
	}
	if !has(m.Step(Event{Kind: Tick}, m.Snapshot().Deadline), Dial) {
		t.Fatalf("fresh client did not dial: %+v", m.Snapshot())
	}
}

func TestAmbientBusyCleanupFailureDelaysReplacement(t *testing.T) {
	for _, cause := range []error{ErrStop, ErrUncertain, ErrReleaseAbandoned} {
		cfg := DefaultConfig()
		cfg.RetryBusy = true
		m := machine(t, cfg)
		m.Step(Event{Kind: Start}, 0)
		ready(m, 0)
		m.Step(Event{Kind: Failed, Attempt: 1, Layer: Rendezvous, Err: ErrTransport}, 1)
		m.Step(Event{Kind: Released, Attempt: 1}, 2)
		now := m.Snapshot().Deadline
		m.Step(Event{Kind: Tick}, now)
		m.Step(Event{Kind: Failed, Attempt: 2, Layer: Cloud, Err: ErrBusy}, now)
		m.Step(Event{Kind: Released, Attempt: 2, Err: cause}, now)
		if m.Snapshot().State != Backoff || m.Snapshot().Deadline != now.Add(OwnershipRecoveryDelay) {
			t.Fatalf("missing recovery delay after refusal cleanup %v: %+v", cause, m.Snapshot())
		}
		if !has(m.Step(Event{Kind: Tick}, m.Snapshot().Deadline), Dial) {
			t.Fatal("did not recover")
		}
	}
}

func TestAmbientTransportFailureAfterBusyRetainsReleaseRecovery(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RetryBusy = true
	m := machine(t, cfg)
	m.Step(Event{Kind: Start}, 0)
	ready(m, 0)
	m.Step(Event{Kind: Failed, Attempt: 1, Layer: Rendezvous, Err: ErrTransport}, 1)
	m.Step(Event{Kind: Released, Attempt: 1}, 2)
	now := m.Snapshot().Deadline
	m.Step(Event{Kind: Tick}, now)
	m.Step(Event{Kind: Failed, Attempt: 2, Layer: Cloud, Err: ErrBusy}, now)
	m.Step(Event{Kind: Released, Attempt: 2}, now)
	now = m.Snapshot().Deadline
	m.Step(Event{Kind: Tick}, now)
	m.Step(Event{Kind: Progress, Attempt: 3, Layer: Cloud}, now)
	m.Step(Event{Kind: Failed, Attempt: 3, Layer: Rendezvous, Err: ErrTransport}, now)
	m.Step(Event{Kind: Released, Attempt: 3, Err: ErrReleaseAbandoned}, now)
	if !has(m.Step(Event{Kind: Tick}, m.Snapshot().Deadline), Dial) {
		t.Fatalf("prior busy blocked transport release recovery: %+v", m.Snapshot())
	}
}

func TestAmbientFirstBusyBackoff(t *testing.T) {
	for _, upper := range []bool{false, true} {
		cfg := DefaultConfig()
		cfg.RetryBusy = true
		var jitter fixedRNG
		m, err := New(cfg, &jitter)
		if err != nil {
			t.Fatal(err)
		}
		m.Step(Event{Kind: Start}, 0)
		var now core.Instant
		cap := time.Second
		for i := 0; i < 2000; i++ {
			lo := cap / 2
			want := lo
			if upper {
				jitter = fixedRNG(cap - lo)
				want = cap
			}
			m.Step(Event{Kind: Failed, Attempt: m.attempt, Layer: Cloud, Err: ErrBusy}, now)
			m.Step(Event{Kind: Released, Attempt: m.attempt}, now)
			next := m.Snapshot().Deadline
			if m.Snapshot().State != Backoff || time.Duration(next-now) != want {
				t.Fatalf("upper=%v refusal=%d state/deadline=%+v, want backoff %v", upper, i, m.Snapshot(), want)
			}
			if !has(m.Step(Event{Kind: Tick}, next), Dial) {
				t.Fatalf("upper=%v refusal=%d did not retry", upper, i)
			}
			now = next
			cap = min(30*time.Second, cap*2)
		}
		if time.Duration(now) < 8*time.Hour {
			t.Fatalf("outage=%v, want beyond 8 hours", time.Duration(now))
		}
		ready(m, now)
		if !has(m.Step(Event{Kind: Client, Client: 1}, now), Admit) {
			t.Fatal("first acquisition after overnight busy did not admit client")
		}
	}
}

func TestAmbientFirstBusyRejectsWaitingClient(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RetryBusy = true
	m := machine(t, cfg)
	m.Step(Event{Kind: Start}, 0)
	m.Step(Event{Kind: Client, Client: 1}, 0)
	as := m.Step(Event{Kind: Failed, Attempt: 1, Layer: Cloud, Err: ErrBusy}, 1)
	if !has(as, Reject) || m.Snapshot().Queued != 0 {
		t.Fatalf("first busy actions/snapshot=%+v/%+v, want queued client rejected", as, m.Snapshot())
	}
	m.Step(Event{Kind: Released, Attempt: 1}, 2)
	if !has(m.Step(Event{Kind: Tick}, m.Snapshot().Deadline), Dial) {
		t.Fatal("rejecting initial demand stopped ambient retries")
	}
}
