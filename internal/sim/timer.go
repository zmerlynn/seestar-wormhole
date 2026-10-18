package sim

import (
	"container/heap"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// Timer adapts an owner's single replaceable deadline to a Scheduler. Arm queues
// the supplied timeout event; Cancel physically removes it. Replacements cannot
// accumulate stale events or consume extra budget. Like Scheduler, it has one
// owner and must not be copied. Searches are bounded by the scheduler event cap.
type Timer[E any] struct {
	scheduler *Scheduler[E]
	order     uint64
}

// NewTimer creates an unarmed timer sharing the scheduler's clock and budgets.
func NewTimer[E any](s *Scheduler[E]) *Timer[E] { return &Timer[E]{scheduler: s} }

// Arm replaces the current timeout with a zero-byte event at the exact deadline.
// The event must not retain payload; schedule payload-bearing input separately.
func (t *Timer[E]) Arm(at core.Instant, event E) error {
	if at < t.scheduler.Now() {
		return ErrTime
	}
	t.Cancel()
	if err := t.scheduler.Schedule(at, event, 0); err != nil {
		return err
	}
	t.order = t.scheduler.serial
	return nil
}

// Cancel removes any outstanding event; canceling an expired timer is harmless.
func (t *Timer[E]) Cancel() {
	for i, v := range t.scheduler.q {
		if v.order == t.order {
			heap.Remove(&t.scheduler.q, i)
			t.scheduler.budget.Release(v.Bytes, 1)
			break
		}
	}
	t.order = 0
}

// Deadline reports the current deadline, or false after firing/cancellation.
func (t *Timer[E]) Deadline() (core.Instant, bool) {
	for _, v := range t.scheduler.q {
		if v.order == t.order {
			return v.At, true
		}
	}
	return 0, false
}
