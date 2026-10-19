package proxy

import (
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"time"
)

// Discard reporting is always on when a logger exists.
const discardInterval = 10 * time.Second

func (x *stack) initDiscardSummary(now core.Instant) {
	if x.discardNext == 0 && x.f != nil && x.f.s != nil && x.f.s.logger != nil && now < core.Instant(1<<63-1) {
		x.discardNext = discardDeadline(now, discardInterval)
	}
}

func (x *stack) discardSummary(now core.Instant) {
	if x.discardNext == 0 || now < x.discardNext {
		return
	}
	x.discardNext = discardDeadline(now, discardInterval)
	if now == core.Instant(1<<63-1) {
		x.discardNext = 0
	}
	x.flushDiscardSummary()
}

// A joined attempt ends its final, possibly short reporting interval.
func (x *stack) flushDiscardSummary() {
	if x.f == nil || x.f.s == nil || x.f.s.logger == nil {
		return
	}
	if x.iotc != nil {
		for i, n := range x.iotc.Discards() {
			x.discardCount(39+i, n)
		}
	}
	if x.reliable != nil {
		for i, n := range x.reliable.Discards() {
			x.discardCount(87+i, n)
		}
	}
	x.discardCount(72, x.ingressDiscards)
	if x.packets != nil {
		for i, n := range x.packets.Discards() {
			x.discardCount([3]int{27, 28, 131}[i], n)
		}
	}
}

func (x *stack) discardCount(rule int, total uint64) {
	count := total - x.discardLast[rule]
	x.discardLast[rule] = total
	if count != 0 {
		x.f.s.logger.Warn("transport_discard_summary", "rule", fmt.Sprintf("R%03d", rule), "count", count)
	}
}

func (s *Session) sessionDiscardSummary(total uint64, now core.Instant) {
	if s.logger == nil {
		return
	}
	if s.discardNext == 0 {
		if total == s.discardLast || now == core.Instant(1<<63-1) {
			return
		}
		s.discardNext = discardDeadline(now, discardInterval)
	}
	if now < s.discardNext {
		return
	}
	s.flushSessionDiscardSummary(total)
}

func (s *Session) flushSessionDiscardSummary(total uint64) {
	if s.logger == nil {
		return
	}
	count := total - s.discardLast
	s.discardLast = total
	s.discardNext = 0
	if count != 0 {
		s.logger.Warn("transport_discard_summary", "rule", "R097", "count", count)
	}
}

// discardDeadline saturates rather than wrapping the owner epoch.
func discardDeadline(now core.Instant, d time.Duration) core.Instant {
	if core.Instant(d) > core.Instant(1<<63-1)-now {
		return core.Instant(1<<63 - 1)
	}
	return now.Add(d)
}

// discardOnlyTick reports discards without injecting protocol events. Due layer
// deadlines still dispatch through the timer worker.
func (x *stack) discardOnlyTick(now core.Instant) bool {
	discardDue := x.discardNext > 0 && now >= x.discardNext
	if !discardDue {
		return false
	}
	for _, at := range x.deadlines {
		if at > 0 && at <= now {
			return false
		}
	}
	x.discardSummary(now)
	x.updateTimer(now)
	return true
}
