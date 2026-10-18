package rdt

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestTelemetryReorderDuplicatePressure(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.Telemetry = true; c.RXBytes = 2000; c.RXPackets = 4 })
	ready(t, m)
	m.Step(incoming(Data, 2, "cc"), core.Instant(time.Millisecond))
	e := incoming(Data, 2, "cc")
	e.Packet.Transmissions = 2
	m.Step(e, core.Instant(2*time.Millisecond))
	s := m.Stats(core.Instant(5 * time.Millisecond))
	if s.AdmittedPackets != 1 || s.AdmittedBytes != 2 || s.Duplicates != 1 || s.RetransmittedObserved != 1 || s.OutOfOrderBytes != 2 || s.GapAge != 4*time.Millisecond || s.OldestGapAge != 4*time.Millisecond || s.Holes != 1 {
		t.Fatalf("reorder stats=%+v", s)
	}
	e = incoming(Data, 3, string(make([]byte, 800)))
	m.Step(e, core.Instant(6*time.Millisecond))
	if s = m.Stats(core.Instant(6 * time.Millisecond)); s.CapacityRejects != 1 || s.AdmittedBytes != 2 {
		t.Fatalf("pressure stats=%+v", s)
	}
	m.Step(incoming(Data, 0, "a"), core.Instant(7*time.Millisecond))
	if s = m.Stats(core.Instant(8 * time.Millisecond)); s.GapAge != time.Millisecond || s.OldestGapAge != 7*time.Millisecond {
		t.Fatalf("moving gap stats=%+v", s)
	}
	m.Step(incoming(Data, 1, "b"), core.Instant(9*time.Millisecond))
	m.Step(Event{Kind: Read, Count: 100}, core.Instant(10*time.Millisecond))
	s = m.Stats(core.Instant(11 * time.Millisecond))
	if s.AdmittedPackets != 3 || s.AdmittedBytes != 4 || s.DeliveredBytes != 4 || s.Holes != 0 || s.GapAge != 0 || s.RXAge != 0 || s.ACKSent == 0 || s.SACKSent == 0 {
		t.Fatalf("drained stats=%+v", s)
	}
}

func TestTelemetryRTTAmbiguityAndRetransmission(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.Telemetry = true })
	ready(t, m)
	m.Step(Event{Kind: Write, Data: []byte("a")}, core.Instant(time.Millisecond))
	m.Step(incoming(ACK2, 0, ""), core.Instant(21*time.Millisecond))
	s := m.Stats(core.Instant(31 * time.Millisecond))
	if s.RTTSamples != 1 || s.RTT != 20*time.Millisecond || s.RTTAge != 10*time.Millisecond || s.RTTAmbiguous != 0 {
		t.Fatalf("RTT stats=%+v", s)
	}
	m.Step(Event{Kind: Write, Data: []byte("b")}, core.Instant(32*time.Millisecond))
	m.Step(Event{Kind: Tick}, core.Instant(time.Second))
	m.Step(incoming(ACK2, 1, ""), core.Instant(time.Second+time.Millisecond))
	s = m.Stats(core.Instant(2 * time.Second))
	if s.RTTSamples != 1 || s.RTTAmbiguous != 1 || s.RetransmittedSent != 1 {
		t.Fatalf("ambiguous stats=%+v", s)
	}
}

// Idle/duplicate paths already have protocol effects; compare allocation counts
// rather than attributing their existing packet/action allocations to telemetry.
func BenchmarkTelemetryDisabled(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "off"
		if enabled {
			name = "on"
		}
		b.Run(name, func(b *testing.B) {
			m := newMachine(b, func(c *Config) { c.Telemetry = enabled })
			ready(b, m)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.Step(Event{Kind: Read, Count: 1}, 0)
			}
		})
	}
}
func TestTelemetryDisabledAllocations(t *testing.T) {
	measure := func(enabled bool) float64 {
		m := newMachine(t, func(c *Config) { c.Telemetry = enabled })
		ready(t, m)
		return testing.AllocsPerRun(1000, func() { m.Step(Event{Kind: Read, Count: 1}, 0) })
	}
	if off, on := measure(false), measure(true); off != 0 || on != off {
		t.Fatalf("allocs off=%v on=%v, want zero", off, on)
	}
}

func BenchmarkTelemetryDuplicate(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "off"
		if enabled {
			name = "on"
		}
		b.Run(name, func(b *testing.B) {
			m := newMachine(b, func(c *Config) { c.Telemetry = enabled })
			ready(b, m)
			e := incoming(Data, 0, "private-payload")
			m.Step(e, 0)
			m.Step(Event{Kind: Read, Count: 100}, 0)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m.Step(e, 0)
			}
		})
	}
}
func TestTelemetryDoesNotAddDuplicateAllocations(t *testing.T) {
	measure := func(enabled bool) float64 {
		m := newMachine(t, func(c *Config) { c.Telemetry = enabled })
		ready(t, m)
		e := incoming(Data, 0, "private-payload")
		m.Step(e, 0)
		m.Step(Event{Kind: Read, Count: 100}, 0)
		return testing.AllocsPerRun(1000, func() { m.Step(e, 0) })
	}
	if off, on := measure(false), measure(true); off != on {
		t.Fatalf("duplicate allocations off=%v on=%v", off, on)
	}
}
