package rdt

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"time"
)

// Stats is an immutable, payload-free snapshot scoped to this RDT instance.
// Counters count successful core handoffs, not physical network receipt. Duplicate
// observations include late originals; the transmission byte only identifies
// retries when >1 (its wrap cannot be inferred by a receiver).
type Stats struct {
	RXPeak, TXPeak int           // lifetime byte peaks; retain excursions missed by snapshots
	GapAgeMax      time.Duration // maximum missing-head excursion, including a live gap
	// CWND is the local DATA window, not the unobservable remote sender window.
	CWND, TXPackets, UnsentPackets, UnsentBytes, InFlightPackets, InFlightBytes int
	// Retry causes partition RetransmittedSent. Force takes precedence over RTO,
	// which takes precedence over urgent pacing when several conditions are due.
	RetransmitSACK, RetransmitACK, RetransmitRTO, RetransmitPacing uint64
	ACKReceived, SACKReceived, SACKRangesReceived                  uint64
	OutOfOrderArrivals, ReorderDistanceTotal, ReorderDistanceMax   uint64

	OldestGapAge                                         time.Duration // oldest retained proof of any receive hole, including a partially filled gap
	AdmittedPackets, AdmittedBytes, DeliveredBytes       uint64
	TXAdmittedPackets, TXAdmittedBytes                   uint64
	Duplicates, RetransmittedObserved, RetransmittedSent uint64
	ACKSent, SACKSent, SACKRangesSent, CapacityRejects   uint64
	RTTSamples, RTTAmbiguous                             uint64
	RTT, RTTAge, SRTT, RTTVar, RTO                       time.Duration
	RXAge, TXAge, GapAge                                 time.Duration
	OutOfOrderBytes, Holes, RXBytes, TXBytes             int
	AdmittedNext, ReadNext                               Sequence
	FeedbackAge                                          time.Duration // age of the last accepted DATA ACK/SACK
	LastACKAge                                           time.Duration
}

type telemetry struct {
	txForce             []Type         // pending feedback cause, aligned with txAt; no packet layout cost off
	rxAt, txAt          []core.Instant // bounded parallel admission timestamps; allocated only when enabled
	stats               Stats
	rttAt, ackAt, gapAt core.Instant
	gap                 Sequence
	feedbackAt          core.Instant
	hasFeedback         bool
	hasGap, hasACK      bool
}

// Stats reads counters without changing protocol state. Disabled machines return
// the zero value without scanning queues. Ages clamp a regressing query clock.
// RXAge/TXAge refer to the oldest retained packet's initial admission, including
// partial consumption; GapAge restarts when the missing admission head moves.
func (m *Machine) Stats(now core.Instant) Stats {
	if m.telemetry == nil {
		return Stats{}
	}
	now = max(now, m.now)
	t := m.telemetry
	s := t.stats
	s.RXBytes, s.TXBytes = m.rxBytes, m.txBytes
	s.CWND = m.window
	s.AdmittedNext, s.ReadNext = m.ackNext, m.nextRead
	s.SRTT, s.RTTVar, s.RTO = m.srtt, m.rttvar, m.rto
	if s.RTTSamples > 0 {
		s.RTTAge = now.Sub(t.rttAt)
	}
	if t.hasFeedback {
		s.FeedbackAge = now.Sub(t.feedbackAt)
	}
	if t.hasACK {
		s.LastACKAge = now.Sub(t.ackAt)
	}
	if t.hasGap {
		s.GapAge = now.Sub(t.gapAt)
		s.GapAgeMax = max(s.GapAgeMax, s.GapAge)
	}
	next := m.ackNext
	for i, p := range m.rx {
		if i == 0 || now.Sub(t.rxAt[i]) > s.RXAge {
			s.RXAge = now.Sub(t.rxAt[i])
		}
		if !p.sequence.Less(m.ackNext) {
			s.OutOfOrderBytes += len(p.data)
			s.OldestGapAge = max(s.OldestGapAge, now.Sub(t.rxAt[i]))
			if next.Less(p.sequence) {
				s.Holes++
			}
			next = p.sequence.Next()
		}
	}
	for i, p := range m.tx {
		if p.packet.Type != FIN {
			s.TXPackets++
			if p.sent {
				s.InFlightPackets++
				s.InFlightBytes += len(p.packet.Payload)
			} else {
				s.UnsentPackets++
				s.UnsentBytes += len(p.packet.Payload)
			}
			s.TXAge = max(s.TXAge, now.Sub(t.txAt[i]))
		}
	}
	return s
}

func (m *Machine) telemetryGap() {
	if m.telemetry == nil {
		return
	}
	t := m.telemetry
	if t.hasGap {
		t.stats.GapAgeMax = max(t.stats.GapAgeMax, m.now.Sub(t.gapAt))
	}
	has := len(m.rx) > 0 && m.ackNext.Less(m.rx[len(m.rx)-1].sequence)
	if has && (!t.hasGap || t.gap != m.ackNext) {
		t.gapAt = m.now
		t.gap = m.ackNext
	}
	t.hasGap = has
}

// Counters reads cumulative counters without scanning live queues. Use Stats
// only when a full queue/age snapshot is needed.
func (m *Machine) Counters() Stats {
	if m.telemetry == nil {
		return Stats{}
	}
	return m.telemetry.stats
}

// reorderDistance measures packets ahead of ackNext using the protocol's
// low-word rollover at f0000000. Unexpected low words above that radix are
// tolerated; adjacent-high backwards encodings report a minimum distance of 1.
func reorderDistance(head, arrival Sequence) uint64 {
	const radix = uint64(0xf0000001)
	a, b := uint64(head.High)*radix+uint64(head.Low), uint64(arrival.High)*radix+uint64(arrival.Low)
	if b <= a {
		return 1
	}
	return b - a
}
