package proxy

import (
	"context"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"log/slog"
	"time"
)

const debugRecordsPerInterval = 64

// TelemetryLossError retains the final detail-cap loss count across attempts.
type TelemetryLossError struct{ Count uint64 }

func (e *TelemetryLossError) Error() string {
	return fmt.Sprintf("proxy: %d telemetry diagnostics lost", e.Count)
}

type waitStats struct {
	count      uint64
	total, max time.Duration
}

func (s *waitStats) add(d time.Duration) { s.count++; s.total += d; s.max = max(s.max, d) }

type channelStats struct {
	pause, resume, wake, admitted, rejects, reclaims uint64
	pauseSent, resumeSent, wakeSent                  uint64
	paused                                           time.Duration
	isPaused                                         bool
	delivery                                         int
	pauseAt, deliveryAt                              core.Instant
}

// transportStats belongs exclusively to the stack owner. No histories or maps
// grow with traffic; per-channel counters survive slot reuse within an attempt.
type transportStats struct {
	queueTimes                         [512]core.Instant
	secureQueueBytes                   int
	secureQueueSizes                   [16]int
	secureQueueHead, secureQueueCount  int
	ingressRejects                     uint64
	channels                           [256]channelStats
	next                               core.Instant
	exhausted                          bool
	debugRecords                       int
	ownerWait, sendWait, sendQueueWait waitStats
}

func (x *stack) initTelemetry(now core.Instant) {
	if x.telemetry == nil && x.f != nil && x.f.s != nil && x.f.s.statsInterval > 0 && x.f.s.logger != nil {
		x.telemetry = &transportStats{next: statsDeadline(now, x.f.s.statsInterval)}
		x.sendMarks = make(chan time.Time, 16)
	}
}

// statsDeadline saturates rather than wrapping the owner epoch. At the final
// representable instant stats emits once, then retires its periodic deadline.
func statsDeadline(now core.Instant, d time.Duration) core.Instant {
	if core.Instant(d) > core.Instant(1<<63-1)-now {
		return core.Instant(1<<63 - 1)
	}
	return now.Add(d)
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
func (x *stack) telemetryEvent(event string, attrs ...slog.Attr) {
	s := x.telemetry
	if s == nil || !x.f.s.logger.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	if s.debugRecords >= debugRecordsPerInterval {
		x.debugDropped.Add(1)
		return
	}
	s.debugRecords++
	// Attrs are constructed only by local callers from numeric counters or a fixed
	// vocabulary. Neither protocol actions nor errors are handed to the logger.
	x.f.s.logger.LogAttrs(context.Background(), slog.LevelDebug, event, attrs...)
}

// transportEvent preserves existing pressure/oddity debug logging when metric
// collection is off. Without an interval it uses a fixed per-attempt log budget;
// it allocates no telemetry state, samples no clock and arms no timer.
func (x *stack) transportEvent(event string, attrs ...slog.Attr) {
	if x.telemetry != nil {
		x.telemetryEvent(event, attrs...)
		return
	}
	if x.f == nil || x.f.s == nil || x.f.s.logger == nil || !x.f.s.logger.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	if x.legacyDebugRecords >= debugRecordsPerInterval {
		x.debugDropped.Add(1)
		// Without periodic stats, report at most once per second of owner time.
		if x.now >= x.legacyDebugNextWarn {
			x.reportTelemetryLoss()
			x.legacyDebugNextWarn = statsDeadline(x.now, time.Second)
		}
		return
	}
	x.legacyDebugRecords++
	x.f.s.logger.LogAttrs(context.Background(), slog.LevelDebug, event, attrs...)
}

// reportTelemetryLoss may run during owner cancellation. Only atomic loss
// counters and the immutable logger cross that shutdown boundary.
func (x *stack) reportTelemetryLoss() {
	if x.f == nil || x.f.s == nil || x.f.s.logger == nil {
		return
	}
	count, reported := x.debugDropped.Load(), x.debugReported.Load()
	if count <= reported || !x.debugReported.CompareAndSwap(reported, count) {
		return
	}
	x.f.s.logger.Warn("diagnostics_lost", "rule", "telemetry_detail_cap", "count", count)
}

func (x *stack) rdtAfter(e rdt.Event, before rdt.Stats, now core.Instant) {
	if x.telemetry == nil {
		return
	}
	s := x.reliable.Counters()
	if s.AdmittedPackets != before.AdmittedPackets || s.Duplicates != before.Duplicates || s.RetransmittedObserved != before.RetransmittedObserved || s.RetransmittedSent != before.RetransmittedSent || s.ACKSent != before.ACKSent || s.SACKSent != before.SACKSent || s.RTTSamples != before.RTTSamples || s.RTTAmbiguous != before.RTTAmbiguous || s.CapacityRejects != before.CapacityRejects {
		if !x.f.s.logger.Enabled(context.Background(), slog.LevelDebug) {
			return
		}
		if x.telemetry.debugRecords >= debugRecordsPerInterval {
			x.debugDropped.Add(1)
			return
		}
		s = x.reliable.Stats(now)
		x.telemetryEvent("rdt_event", slog.Int("event", int(e.Kind)), slog.Uint64("admitted_packets", s.AdmittedPackets-before.AdmittedPackets), slog.Uint64("admitted_bytes", s.AdmittedBytes-before.AdmittedBytes), slog.Uint64("duplicates", s.Duplicates-before.Duplicates), slog.Uint64("retransmitted_observed", s.RetransmittedObserved-before.RetransmittedObserved), slog.Uint64("retransmitted_sent", s.RetransmittedSent-before.RetransmittedSent), slog.Uint64("ack_sent", s.ACKSent-before.ACKSent), slog.Uint64("sack_sent", s.SACKSent-before.SACKSent), slog.Uint64("capacity_rejects", s.CapacityRejects-before.CapacityRejects), slog.Int("holes", s.Holes), slog.Float64("head_gap_age_ms", milliseconds(s.GapAge)), slog.Float64("oldest_gap_age_ms", milliseconds(s.OldestGapAge)), slog.Float64("rtt_ms", milliseconds(s.RTT)), slog.Float64("rtt_age_ms", milliseconds(s.RTTAge)), slog.Uint64("rtt_samples", s.RTTSamples), slog.Uint64("rtt_ambiguous", s.RTTAmbiguous))
	}
}

func (x *stack) tunnelAfter(now core.Instant) {
	if x.telemetry == nil {
		return
	}
	x.tunnel.VisitTelemetryChanges(now, func(id byte, v p2ptunnel.ChannelTelemetry) {
		s := &x.telemetry.channels[id]
		pause, resume, wake := v.Pause-s.pause, v.Resume-s.resume, v.Wake-s.wake
		decisions := pause + resume + wake
		cmd, reason := p2ptunnel.Command(0), ""
		switch {
		case decisions > 1:
			// The slot view retains only the final decision's Q. Describe all
			// retained transitions without assigning that Q to an earlier one.
			reason = "mixed_decisions"
		case pause == 1:
			cmd, reason = p2ptunnel.PauseCommand, "high_water"
		case resume == 1:
			cmd, reason = p2ptunnel.ResumeCommand, "low_water"
		case wake == 1:
			cmd, reason = p2ptunnel.WakeCommand, "drain_opportunity"
		}
		s.pause, s.resume, s.wake = v.Pause, v.Resume, v.Wake
		s.pauseSent, s.resumeSent, s.wakeSent = v.PauseSent, v.ResumeSent, v.WakeSent
		s.admitted, s.rejects, s.reclaims = v.AdmittedBytes, v.Rejects, v.Reclaims
		if v.IsPaused && !s.isPaused {
			s.pauseAt = now
		}
		if !v.IsPaused {
			s.pauseAt = 0
		}
		s.paused, s.isPaused, s.delivery = v.Paused, v.IsPaused, v.DeliveryBytes
		s.deliveryAt = 0
		if v.DeliveryBytes > 0 {
			s.deliveryAt = now.Add(-v.DeliveryAge)
		}
		if decisions != 0 {
			x.telemetryEvent("tunnel_flow", slog.Int("channel", int(id)), slog.Int("event", int(cmd)), slog.String("reason", reason), slog.Int("decision_q", v.DecisionQ), slog.Bool("decision_q_exact", decisions == 1), slog.Uint64("pause_delta", pause), slog.Uint64("resume_delta", resume), slog.Uint64("wake_delta", wake))
		}
	})
}

func (x *stack) stats(now core.Instant) {
	s := x.telemetry
	if s == nil || s.exhausted || now < s.next {
		return
	}
	s.next = statsDeadline(now, x.f.s.statsInterval)
	if now == core.Instant(1<<63-1) {
		s.next = 0
		s.exhausted = true
	}
	s.debugRecords = 0
	x.reportTelemetryLoss()
	logger := x.f.s.logger
	if !logger.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	logger.LogAttrs(context.Background(), slog.LevelDebug, "transport_stats", slog.String("scope", "session"), slog.Uint64("debug_dropped", x.debugDropped.Load()), slog.Uint64("owner_wait_count", s.ownerWait.count), slog.Float64("owner_wait_total_ms", milliseconds(s.ownerWait.total)), slog.Float64("owner_wait_max_ms", milliseconds(s.ownerWait.max)), slog.Uint64("edge_send_wait_count", s.sendWait.count), slog.Float64("edge_send_wait_total_ms", milliseconds(s.sendWait.total)), slog.Float64("edge_send_wait_max_ms", milliseconds(s.sendWait.max)), slog.Uint64("edge_send_queue_wait_count", s.sendQueueWait.count), slog.Float64("edge_send_queue_wait_total_ms", milliseconds(s.sendQueueWait.total)), slog.Float64("edge_send_queue_wait_max_ms", milliseconds(s.sendQueueWait.max)), slog.Int("edge_send_queue_bytes", s.secureQueueBytes), slog.Int("owner_queue_count", len(x.f.s.work)), slog.Int("layer_queue_bytes", x.ownerQueueBytes()), slog.Float64("layer_queue_age_ms", milliseconds(x.ownerQueueAge(now))), slog.Uint64("ingress_capacity_rejects", s.ingressRejects))
	if x.reliable != nil {
		r := x.reliable.Stats(now)
		logger.LogAttrs(context.Background(), slog.LevelDebug, "rdt_stats", slog.String("scope", "session"), slog.Int("cwnd_packets", r.CWND), slog.Int("rx_peak_bytes", r.RXPeak), slog.Int("tx_peak_bytes", r.TXPeak), slog.Float64("head_gap_age_max_ms", milliseconds(r.GapAgeMax)), slog.Int("tx_packets", r.TXPackets), slog.Int("unsent_packets", r.UnsentPackets), slog.Int("unsent_bytes", r.UnsentBytes), slog.Int("in_flight_packets", r.InFlightPackets), slog.Int("in_flight_bytes", r.InFlightBytes), slog.Uint64("retransmit_sack", r.RetransmitSACK), slog.Uint64("retransmit_ack", r.RetransmitACK), slog.Uint64("retransmit_rto", r.RetransmitRTO), slog.Uint64("retransmit_pacing", r.RetransmitPacing), slog.Uint64("ack_received", r.ACKReceived), slog.Uint64("sack_received", r.SACKReceived), slog.Uint64("sack_ranges_received", r.SACKRangesReceived), slog.Uint64("out_of_order_arrivals", r.OutOfOrderArrivals), slog.Uint64("reorder_distance_total", r.ReorderDistanceTotal), slog.Uint64("reorder_distance_max", r.ReorderDistanceMax), slog.Float64("feedback_age_ms", milliseconds(r.FeedbackAge)), slog.Uint64("admitted_packets", r.AdmittedPackets), slog.Uint64("admitted_bytes", r.AdmittedBytes), slog.Uint64("delivered_bytes", r.DeliveredBytes), slog.Uint64("tx_admitted_packets", r.TXAdmittedPackets), slog.Uint64("tx_admitted_bytes", r.TXAdmittedBytes), slog.Uint64("duplicates", r.Duplicates), slog.Uint64("retransmitted_observed", r.RetransmittedObserved), slog.Uint64("retransmitted_sent", r.RetransmittedSent), slog.Uint64("ack_sent", r.ACKSent), slog.Uint64("sack_sent", r.SACKSent), slog.Uint64("sack_ranges_sent", r.SACKRangesSent), slog.Float64("ack_age_ms", milliseconds(r.LastACKAge)), slog.Int("holes", r.Holes), slog.Int("out_of_order_bytes", r.OutOfOrderBytes), slog.Float64("head_gap_age_ms", milliseconds(r.GapAge)), slog.Float64("oldest_gap_age_ms", milliseconds(r.OldestGapAge)), slog.Uint64("rtt_samples", r.RTTSamples), slog.Uint64("rtt_ambiguous", r.RTTAmbiguous), slog.Float64("rtt_ms", milliseconds(r.RTT)), slog.Float64("rtt_age_ms", milliseconds(r.RTTAge)), slog.Float64("srtt_ms", milliseconds(r.SRTT)), slog.Float64("rttvar_ms", milliseconds(r.RTTVar)), slog.Float64("rto_ms", milliseconds(r.RTO)), slog.Int("rx_bytes", r.RXBytes), slog.Float64("rx_age_ms", milliseconds(r.RXAge)), slog.Int("tx_bytes", r.TXBytes), slog.Float64("tx_age_ms", milliseconds(r.TXAge)), slog.Uint64("capacity_rejects", r.CapacityRejects), slog.Uint64("admitted_next_low", uint64(r.AdmittedNext.Low)), slog.Uint64("admitted_next_high", uint64(r.AdmittedNext.High)), slog.Uint64("read_next_low", uint64(r.ReadNext.Low)), slog.Uint64("read_next_high", uint64(r.ReadNext.High)))
	}
	if x.tunnel != nil {
		for i := range s.channels {
			c := x.tunnel.Channel(byte(i))
			v := x.tunnel.QueueStats(byte(i), now)
			ch := s.channels[i]
			native := x.tunnel.Telemetry(byte(i), now)
			if !c.Active && ch.pause+ch.resume+ch.wake+ch.admitted+ch.rejects+native.PauseReceived+native.ResumeReceived+native.WakeReceived == 0 {
				continue
			}
			paused, age := native.Paused, native.DeliveryAge
			logger.LogAttrs(context.Background(), slog.LevelDebug, "tunnel_stats", slog.String("scope", "channel"), slog.Int("channel", i), slog.Uint64("pause_sent", ch.pauseSent), slog.Uint64("resume_sent", ch.resumeSent), slog.Uint64("wake_sent", ch.wakeSent), slog.Uint64("pause_queued", ch.pause), slog.Uint64("resume_queued", ch.resume), slog.Uint64("wake_queued", ch.wake), slog.Float64("paused_ms", milliseconds(paused)), slog.Int("rx_bytes", v.RXBytes), slog.Float64("rx_backlog_age_ms", milliseconds(v.BacklogAge)), slog.Float64("rx_progress_age_ms", milliseconds(v.ProgressAge)), slog.Int("delivery_bytes", native.DeliveryBytes), slog.Float64("delivery_age_ms", milliseconds(age)), slog.Uint64("admitted_bytes", native.AdmittedBytes), slog.Uint64("delivered_bytes", native.DeliveredBytes), slog.Uint64("discarded_bytes", native.DiscardedBytes), slog.Uint64("admitted_packets", native.AdmittedPackets), slog.Int("high_water", native.HighWater), slog.Int("low_water", native.LowWater), slog.Int("decision_q", native.DecisionQ), slog.Int("decision_q_max", native.DecisionQMax), slog.Int("rx_peak_bytes", native.RXPeak), slog.Bool("paused", native.IsPaused), slog.Bool("accepted_paused", native.AcceptedPaused), slog.Bool("wake_pending", native.WakePending), slog.Int("pending_control", int(native.PendingControl)), slog.Int("handoff_control", int(native.HandoffControl)), slog.Int("handoff_accepted_bytes", native.HandoffAcceptedBytes), slog.Int("pressure_q", native.PressureQ), slog.Int("attributed_rx_bytes", native.AttributedRXBytes), slog.Uint64("pause_received", native.PauseReceived), slog.Uint64("resume_received", native.ResumeReceived), slog.Uint64("wake_received", native.WakeReceived), slog.Int("local_read_state", int(c.ReadState)), slog.Uint64("capacity_rejects", ch.rejects), slog.Uint64("capacity_reclaims", ch.reclaims))
		}
	}
	s.debugRecords = 0
}
func (x *stack) ownerQueueBytes() int {
	bytes := 0
	for _, e := range x.queue {
		if e.r != nil {
			bytes += len(e.r.Data) + len(e.r.Packet.Payload)
		}
		if e.t != nil {
			bytes += len(e.t.Data)
		}
		if e.i != nil {
			bytes += len(e.i.Data)
		}
	}
	return bytes
}

func (x *stack) tunnelStep(e p2ptunnel.Event, now core.Instant) []p2ptunnel.Action {
	if x.telemetry == nil {
		return x.tunnel.Step(e, now)
	}
	x.tunnel.EnableTelemetry()
	actions := x.tunnel.Step(e, now)
	x.tunnelAfter(now)
	return actions
}

func (x *stack) ownerQueueAge(now core.Instant) time.Duration {
	oldest := time.Duration(0)
	for i := range x.queue {
		oldest = max(oldest, now.Sub(x.telemetry.queueTimes[i]))
	}
	return oldest
}

// telemetryOnlyTick reports idle stats and discards without protocol events. The
// existing timer worker still dispatches actual due layer deadlines normally.
func (x *stack) telemetryOnlyTick(now core.Instant) bool {
	discardDue := x.discardNext > 0 && now >= x.discardNext
	statsDue := x.telemetry != nil && !x.telemetry.exhausted && now >= x.telemetry.next
	if !discardDue && !statsDue {
		return false
	}
	for _, at := range x.deadlines {
		if at > 0 && at <= now {
			return false
		}
	}
	x.discardSummary(now)
	x.stats(now)
	x.updateTimer(now)
	return true
}
