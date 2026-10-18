package core

import "time"

// Instant is elapsed monotonic nanoseconds in an owner-defined epoch, never UTC.
// Adapters must keep values nonnegative and within time.Duration's range.
type Instant time.Duration

func (t Instant) Add(d time.Duration) Instant { return t + Instant(d) }
func (t Instant) Sub(u Instant) time.Duration { return time.Duration(t - u) }

// Machine must not retain mutable event storage or expose mutable internal storage.
// Step is synchronous, bounded, and called by exactly one owner. Actions describe
// effects; completion arrives as another event. Neither side may block the owner.
type Machine[E, A any] interface {
	Step(event E, now Instant) []A
}

// Budget bounds both bytes and object counts. Zero is a zero budget, not unlimited.
type Budget struct {
	MaxBytes, MaxCount int
	Bytes, Count       int
}

func (b *Budget) Reserve(n, count int) bool {
	if n < 0 || count < 0 || n > b.MaxBytes-b.Bytes || count > b.MaxCount-b.Count {
		return false
	}
	b.Bytes += n
	b.Count += count
	return true
}
func (b *Budget) Release(n, count int) {
	if n < 0 || count < 0 || n > b.Bytes || count > b.Count {
		panic("invalid budget release")
	}
	b.Bytes -= n
	b.Count -= count
}
