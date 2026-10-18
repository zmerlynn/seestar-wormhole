package proxy

import (
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"time"
)

// Loss reporting is always on when a logger exists, independently of DEBUG stats.
const discardInterval = 10 * time.Second

func (x *stack) initDiscardSummary(now core.Instant) {
	if x.discardNext == 0 && x.f != nil && x.f.s != nil && x.f.s.logger != nil && now < core.Instant(1<<63-1) {
		x.discardNext = statsDeadline(now, discardInterval)
	}
}

func (x *stack) discardSummary(now core.Instant) {
	if x.discardNext == 0 || now < x.discardNext {
		return
	}
	x.discardNext = statsDeadline(now, discardInterval)
	if now == core.Instant(1<<63-1) {
		x.discardNext = 0
	}
	x.flushDiscardSummary()
	if x.telemetry == nil {
		x.reportTelemetryLoss()
	}
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
		s.discardNext = statsDeadline(now, discardInterval)
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
