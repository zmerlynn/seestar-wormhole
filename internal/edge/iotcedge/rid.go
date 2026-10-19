package iotcedge

import (
	"io"
	"sync"
)

// Default redial backoff settles at 15–30s (session.DefaultConfig/RetryBackoff):
// at most 86400/15 = 5760 attempts/day once settled. Lazy-refusal backoff starts
// at 2–4s and settles at 7.5–15s; the settled 7.5s floor gives 11520/day:
// 2^24 / 11520 / 365 is about four years.
// A 60s lazy idle cycle gives 1440/day, about 32 years. These are sizing estimates,
// not rate limits; rapid successful flapping or shorter idle settings can burn
// reservations faster. Allocate lazily, never evict; process restart clears all
// history and restores the budget. This is local policy, not a protocol limit.
const maxRIDHistory = 1 << 24

// RIDHistoryExhaustedError reports the process-lifetime reservation ceiling.
// It carries no connection material and remains an ErrIOTCCrypto for callers
// that classify all failed cryptographic effects together.
type RIDHistoryExhaustedError struct{}

// Error returns a fixed diagnostic with process-restart guidance.
func (*RIDHistoryExhaustedError) Error() string {
	return "iotc: RID history exhausted; restart process to resume session setup"
}

// Unwrap preserves the general cryptographic-effect failure classification.
func (*RIDHistoryExhaustedError) Unwrap() error { return ErrIOTCCrypto }

// RID history belongs to the process, across workers and Clear. This is local
// policy preventing reuse of a closed or refused RID; the native implementation
// uses a counter/MAC scheme instead. Entries are never expired, including after a code-4 refusal's 20000 ms window.
// Process-lifetime non-reuse is stronger local policy than the native recent-RID list:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#attempt-and-cache-lifetimes
var processRIDs ridHistory

type ridHistory struct {
	mu    sync.Mutex // Protects used; never held over entropy reads or I/O.
	used  map[[8]byte]struct{}
	limit int // Test-only override, immutable during use; zero selects maxRIDHistory.
}

func (h *ridHistory) generate(rng io.Reader) ([8]byte, error) {
	limit := h.limit
	if limit == 0 {
		limit = maxRIDHistory
	}
	h.mu.Lock()
	full := len(h.used) >= limit
	h.mu.Unlock()
	if full {
		return [8]byte{}, &RIDHistoryExhaustedError{}
	}
	// Bound work if an entropy source repeatedly returns zero or an old RID.
	for range 32 {
		var rid [8]byte
		if _, err := io.ReadFull(rng, rid[:]); err != nil {
			return [8]byte{}, ErrIOTCCrypto
		}
		if rid == [8]byte{} { // The core reserves zero for missing material.
			continue
		}
		h.mu.Lock()
		// Another caller may have filled the last slot during our entropy read.
		if len(h.used) >= limit {
			h.mu.Unlock()
			return [8]byte{}, &RIDHistoryExhaustedError{}
		}
		_, exists := h.used[rid]
		if !exists {
			if h.used == nil {
				h.used = make(map[[8]byte]struct{})
			}
			h.used[rid] = struct{}{}
		}
		h.mu.Unlock()
		if !exists {
			return rid, nil
		}
	}
	return [8]byte{}, ErrIOTCCrypto
}
