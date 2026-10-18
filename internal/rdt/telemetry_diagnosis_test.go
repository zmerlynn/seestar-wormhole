package rdt

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestTelemetryWindowFeedbackAndRetryCauses(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.Telemetry = true; c.MaxPayload = 100; c.InitialWindow = 2 })
	ready(t, m)
	m.Step(Event{Kind: Write, Data: bytes.Repeat([]byte{'x'}, 400)}, core.Instant(time.Millisecond))
	s := m.Stats(core.Instant(11 * time.Millisecond))
	if s.CWND != 2 || s.TXPackets != 4 || s.UnsentPackets != 2 || s.UnsentBytes != 200 || s.InFlightPackets != 2 || s.InFlightBytes != 200 {
		t.Fatalf("window snapshot=%+v", s)
	}
	// Literal SACK payload: two single-packet ranges (1,1) and (3,3).
	b := make([]byte, 72)
	b[4] = 2
	b[8] = 1
	b[16] = 1
	b[24] = 3
	b[32] = 3
	e := incoming(SACK2, 0, "")
	e.Packet.Payload = b
	m.Step(e, core.Instant(21*time.Millisecond))
	s = m.Stats(core.Instant(21 * time.Millisecond))
	if s.SACKReceived != 1 || s.SACKRangesReceived != 2 || s.RetransmitSACK != 1 || s.RetransmitRTO != 0 || s.RetransmitPacing != 0 || s.ACKReceived != 0 {
		t.Fatalf("SACK feedback=%+v", s)
	}
	m.Step(Event{Kind: Tick}, core.Instant(time.Second))
	s = m.Stats(core.Instant(time.Second))
	if s.RetransmitRTO == 0 || s.RetransmittedSent != s.RetransmitSACK+s.RetransmitACK+s.RetransmitRTO+s.RetransmitPacing {
		t.Fatalf("retry partition=%+v", s)
	}
	m.Step(incoming(ACK2, 3, ""), core.Instant(time.Second+time.Millisecond))
	s = m.Stats(core.Instant(2 * time.Second))
	if s.ACKReceived != 1 || s.TXPackets != 0 || s.InFlightBytes != 0 || s.UnsentBytes != 0 {
		t.Fatalf("drained=%+v", s)
	}
}

func TestTelemetryUrgentPacing(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.Telemetry = true })
	ready(t, m)
	m.Step(Event{Kind: WriteUrgent, Data: []byte("x")}, core.Instant(time.Millisecond))
	m.Step(Event{Kind: Tick}, core.Instant(21*time.Millisecond))
	if s := m.Stats(core.Instant(21 * time.Millisecond)); s.RetransmitPacing != 1 || s.RetransmitRTO != 0 {
		t.Fatalf("pacing=%+v", s)
	}
}

func TestTelemetryReorderDistanceAndRanges(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.Telemetry = true })
	ready(t, m)
	m.Step(incoming(Data, 3, "x"), core.Instant(time.Millisecond))
	m.Step(incoming(Data, 2, "y"), core.Instant(4*time.Millisecond))
	m.Step(incoming(Data, 3, "x"), core.Instant(5*time.Millisecond))
	m.Step(Event{Kind: Tick}, core.Instant(104*time.Millisecond))
	s := m.Stats(core.Instant(104 * time.Millisecond))
	if s.OutOfOrderArrivals != 3 || s.ReorderDistanceMax != 3 || s.ReorderDistanceTotal != 8 || s.SACKRangesSent != s.SACKSent || s.SACKSent != 2 {
		t.Fatalf("reorder=%+v", s)
	}
	// Low-word rollover uses the protocol sequence radix, not uint64 subtraction.
	m.ackNext = Sequence{0xf0000000, 10}
	m.nextRead = m.ackNext
	e := incoming(Data, 1, "z")
	e.Packet.Sequence.High = 11
	m.Step(e, core.Instant(105*time.Millisecond))
	if got := m.Counters().ReorderDistanceTotal; got != 10 {
		t.Fatalf("rollover distance total=%d want 10", got)
	}
}

func TestTelemetryOffStorage(t *testing.T) {
	m := newMachine(t, nil)
	if m.telemetry != nil {
		t.Fatal("disabled timestamp/counter storage allocated")
	}
	if s := m.Stats(core.Instant(time.Second)); s != (Stats{}) {
		t.Fatalf("off snapshot=%+v", s)
	}
}

func TestTelemetryPeaksSurviveDrain(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.Telemetry = true })
	ready(t, m)
	m.Step(Event{Kind: Write, Data: []byte("abc")}, core.Instant(time.Millisecond))
	m.Step(incoming(ACK2, 0, ""), core.Instant(21*time.Millisecond))
	m.Step(incoming(Data, 2, "zz"), core.Instant(22*time.Millisecond))
	m.Step(incoming(Data, 0, "a"), core.Instant(32*time.Millisecond))
	m.Step(incoming(Data, 1, "b"), core.Instant(42*time.Millisecond))
	m.Step(Event{Kind: Read, Count: 4}, core.Instant(43*time.Millisecond))
	s := m.Stats(core.Instant(100 * time.Millisecond))
	if s.RXPeak != 4 || s.TXPeak != 3 || s.GapAgeMax != 10*time.Millisecond || s.RXBytes != 0 || s.GapAge != 0 {
		t.Fatalf("drained peak=%+v", s)
	}
}
