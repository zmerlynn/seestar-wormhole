package p2ptunnel

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"math/bits"
	"time"
)

// QueueStats is a payload-free channel view. BacklogAge measures the current
// nonempty excursion, not the residence time of its oldest surviving byte.
// DeliveryBytes includes the outstanding local write (still charged in RXBytes).
type QueueStats struct {
	RXBytes, SharedRXBytes, DeliveryBytes, HighWater int
	PressureQ, AttributedRXBytes                     int
	BacklogAge, ProgressAge                          time.Duration
}

// QueueStats reads existing accounting without instrumenting the flow policy.
func (m *Machine) QueueStats(id byte, now core.Instant) QueueStats {
	c := &m.channels[id]
	now = max(now, m.now)
	s := QueueStats{RXBytes: c.queue.size, SharedRXBytes: m.rdtBytes, DeliveryBytes: c.writeCount, HighWater: m.cfg.HighWater}
	s.PressureQ = m.flowQueue(id)
	s.AttributedRXBytes = s.PressureQ - s.RXBytes
	if c.queue.size > 0 {
		s.BacklogAge = now.Sub(c.queuedAt)
		s.ProgressAge = now.Sub(max(c.queuedAt, c.lastWrite))
	}
	return s
}

// ChannelTelemetry is a bounded, payload-free view of one slot. Totals survive
// slot reuse within a Machine; ages and paused time follow accepted core events.
// DecisionQ is the exact Q at the last successfully queued flow decision.
type ChannelTelemetry struct {
	Pause, Resume, Wake, PauseSent, ResumeSent, WakeSent uint64
	PauseReceived, ResumeReceived, WakeReceived          uint64
	DeliveredBytes, DiscardedBytes                       uint64
	AdmittedPackets, AdmittedBytes, Rejects, Reclaims    uint64
	DecisionQ, DecisionQMax, HighWater, LowWater, RXPeak int
	// IsPaused is desired PAUSE; AcceptedPaused is the last fully accepted
	// state. PendingControl is the coalesced successor relative to that state.
	IsPaused, AcceptedPaused, WakePending bool
	PressureQ, AttributedRXBytes          int
	PendingControl, HandoffControl        Command
	HandoffAcceptedBytes                  int
	Paused, DeliveryAge                   time.Duration
	DeliveryBytes                         int
}

type channelTelemetry struct {
	ChannelTelemetry
	pauseAt, writeAt core.Instant
}

type tunnelTelemetry struct {
	channels [256]channelTelemetry
	dirty    [4]uint64
}

// EnableTelemetry starts owner-only observation. Call before the first observed
// Step. It is idempotent; counters allocated here never retain peer data. Normal
// Steps with telemetry disabled allocate no observation state and do no scans.
func (m *Machine) EnableTelemetry() {
	if m.telemetry != nil {
		return
	}
	m.telemetry = &tunnelTelemetry{}
	for i := range m.channels {
		c := &m.channels[i]
		if c.active {
			t := &m.telemetry.channels[i]
			t.IsPaused, t.pauseAt, t.writeAt = c.sentPause, m.now, m.now
			m.telemetryChanged(byte(i))
		}
	}
}

// Telemetry returns the current slot view without changing policy or clocks.
// A terminal write or release always reports zero delivery bytes and age.
func (m *Machine) Telemetry(id byte, now core.Instant) ChannelTelemetry {
	if m.telemetry == nil {
		return ChannelTelemetry{}
	}
	now = max(now, m.now)
	t := &m.telemetry.channels[id]
	s := t.ChannelTelemetry
	s.HighWater, s.LowWater = m.cfg.HighWater, 4*m.cfg.HighWater/5
	c := &m.channels[id]
	s.IsPaused = c.active && c.sentPause
	s.AcceptedPaused = c.active && c.handedPause
	s.WakePending = c.active && c.wakePending
	s.PendingControl = m.pendingFlow(id)
	s.PressureQ = m.flowQueue(id)
	s.AttributedRXBytes = s.PressureQ - c.queue.size
	if len(m.flowTX.data) > 0 && m.flowID == id && c.active && c.generation == m.flowGeneration {
		s.HandoffControl = m.flowCommand
		s.HandoffAcceptedBytes = m.flowTX.offset
	}
	s.DeliveryBytes = c.writeCount
	if s.DeliveryBytes > 0 {
		s.DeliveryAge = now.Sub(t.writeAt)
	}
	if s.IsPaused {
		s.Paused += now.Sub(t.pauseAt)
	}
	return s
}

// VisitTelemetryChanges drains a fixed 256-bit set of affected slots. Each slot
// is visited once with its final state and exact cumulative decision counters,
// even if one Stream admits DATA, queues PAUSE and releases the slot. No outbound
// queue indexing or callback into policy is involved. Visit must not call Step.
func (m *Machine) VisitTelemetryChanges(now core.Instant, visit func(byte, ChannelTelemetry)) {
	if m.telemetry == nil {
		return
	}
	for i, word := range m.telemetry.dirty {
		m.telemetry.dirty[i] = 0
		for word != 0 {
			bit := bits.TrailingZeros64(word)
			id := byte(i*64 + bit)
			word &= word - 1
			visit(id, m.Telemetry(id, now))
		}
	}
}

func (m *Machine) telemetryChanged(id byte) {
	if m.telemetry != nil {
		m.telemetry.dirty[id/64] |= uint64(1) << (id % 64)
	}
}

// telemetryFlow is the narrow integration hook: invoke only after a control is
// successfully retained, passing the exact policy Q. A coalescing flow store
// must define its decision/retention boundary here, and use telemetrySent only
// on full acceptance of the corresponding retained frame.
func (m *Machine) telemetryFlow(cmd Command, id byte, q int) {
	if m.telemetry == nil {
		return
	}
	t := &m.telemetry.channels[id]
	switch cmd {
	case PauseCommand:
		t.Pause++
		if !t.IsPaused {
			t.IsPaused = true
			t.pauseAt = m.now
		}
	case ResumeCommand:
		t.Resume++
		m.telemetryEndPause(id)
	case WakeCommand:
		t.Wake++
	default:
		return
	}
	t.DecisionQ, t.DecisionQMax = q, max(t.DecisionQMax, q)
	m.telemetryChanged(id)
}

func (m *Machine) telemetryEndPause(id byte) {
	if m.telemetry == nil {
		return
	}
	t := &m.telemetry.channels[id]
	if t.IsPaused {
		t.Paused += m.now.Sub(t.pauseAt)
		t.IsPaused = false
	}
	m.telemetryChanged(id)
}

func (m *Machine) telemetrySent(cmd Command, id byte, size int) {
	if m.telemetry == nil || size != 0 {
		return
	} // opcode 07 LOGIN is not WAKE
	t := &m.telemetry.channels[id]
	switch cmd {
	case PauseCommand:
		t.PauseSent++
	case ResumeCommand:
		t.ResumeSent++
	case WakeCommand:
		t.WakeSent++
	default:
		return
	}
	m.telemetryChanged(id)
}

func (m *Machine) telemetryAdmission(id byte, count int) {
	if m.telemetry == nil {
		return
	}
	t := &m.telemetry.channels[id]
	t.AdmittedPackets++
	t.AdmittedBytes += uint64(count)
	t.RXPeak = max(t.RXPeak, m.channels[id].queue.size)
	m.telemetryChanged(id)
}

func (m *Machine) telemetryReject(id byte) {
	if m.telemetry != nil {
		m.telemetry.channels[id].Rejects++
		m.telemetryChanged(id)
	}
}
