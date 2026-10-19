package iotcedge

import (
	"bytes"
	"errors"
	"testing"
)

const ridExhaustionMessage = "iotc: RID history exhausted; restart process to resume session setup"

func TestRIDHistoryBudgetExhaustion(t *testing.T) {
	history := ridHistory{limit: 2}
	for _, want := range [][8]byte{{1}, {2}} {
		got, err := history.generate(bytes.NewReader(want[:]))
		if err != nil || got != want {
			t.Fatalf("reservation RID=%x err=%v, want %x", got, err, want)
		}
	}
	for _, raw := range [][]byte{{3, 0, 0, 0, 0, 0, 0, 0}, {1}, make([]byte, 256)} {
		rng := bytes.NewReader(raw)
		got, err := history.generate(rng)
		if got != [8]byte{} || !errors.Is(err, ErrIOTCCrypto) {
			t.Fatalf("full history RID=%x err=%v, want zero and exhaustion failure", got, err)
		}
		if err.Error() != ridExhaustionMessage {
			t.Fatalf("exhaustion diagnostic=%q, want %q", err, ridExhaustionMessage)
		}
		if rng.Len() != len(raw) {
			t.Fatal("full history consumed entropy")
		}
	}
	if len(history.used) != 2 {
		t.Fatalf("retained count=%d, want 2", len(history.used))
	}
	for _, old := range [][8]byte{{1}, {2}} {
		if _, ok := history.used[old]; !ok {
			t.Fatalf("reservation %x was evicted", old)
		}
	}
}

type ridReadFunc func([]byte) (int, error)

func (f ridReadFunc) Read(b []byte) (int, error) { return f(b) }

func TestRIDHistoryBudgetConcurrentExhaustion(t *testing.T) {
	const workers, budget = 16, 3
	history := ridHistory{limit: budget}
	ready, release := make(chan struct{}, workers), make(chan struct{})
	type result struct {
		rid [8]byte
		err error
	}
	results := make(chan result, workers)
	for i := range workers {
		go func() {
			rng := ridReadFunc(func(b []byte) (int, error) {
				ready <- struct{}{}
				<-release
				return copy(b, []byte{byte(i + 1), 0, 0, 0, 0, 0, 0, 0}), nil
			})
			rid, err := history.generate(rng)
			results <- result{rid, err}
		}()
	}
	// All calls pass their initial check before any can reserve. The insertion
	// check must enforce the ceiling even with distinct simultaneous draws.
	for range workers {
		<-ready
	}
	close(release)
	seen := make(map[[8]byte]bool)
	for range workers {
		r := <-results
		if r.err != nil {
			if r.rid != [8]byte{} || !errors.Is(r.err, ErrIOTCCrypto) || r.err.Error() != ridExhaustionMessage {
				t.Errorf("losing reservation RID=%x err=%v, want exhaustion", r.rid, r.err)
			}
			continue
		}
		if r.rid == [8]byte{} || seen[r.rid] {
			t.Errorf("reserved zero or duplicate RID=%x", r.rid)
		}
		seen[r.rid] = true
	}
	if len(seen) != budget || len(history.used) != budget {
		t.Fatalf("successful/retained reservations=%d/%d, want %d/%d", len(seen), len(history.used), budget, budget)
	}
	for rid := range seen {
		if _, ok := history.used[rid]; !ok {
			t.Errorf("successful reservation %x was evicted", rid)
		}
	}
}
