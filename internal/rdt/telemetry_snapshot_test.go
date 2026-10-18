package rdt

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestSnapshotAndBoundedACK(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.Telemetry = true; c.SendCapacity = 2; c.TXPackets = 8 })
	ready(t, m)
	// Drain startup's handoffs, using real encoded Send outputs.
	for range 2 {
		m.Step(Event{Kind: SendComplete}, 0)
	}
	aa := m.Step(Event{Kind: Write, Data: bytes.Repeat([]byte{'x'}, 2000)}, core.Instant(time.Millisecond))
	sends := 0
	for _, a := range aa {
		if a.Kind == Send {
			sends++
		}
	}
	s := m.Stats(core.Instant(11 * time.Millisecond))
	if s.TXAdmittedPackets != 2 || s.TXAdmittedBytes != 2000 || s.TXBytes != 2000 || s.TXAge != 10*time.Millisecond || s.RTO != m.RTO() {
		t.Fatalf("TX snapshot=%+v", s)
	}
	if sends != 2 {
		t.Fatalf("DATA wire sends=%d want 2", sends)
	}
	// No outbound slot: incoming DATA creates a deferred ACK, not a handoff.
	m.Step(incoming(Data, 0, "ab"), core.Instant(21*time.Millisecond))
	before := m.Counters().ACKSent
	if before != 0 {
		t.Fatalf("ACK counted before handoff: %d", before)
	}
	aa = m.Step(Event{Kind: SendComplete}, core.Instant(22*time.Millisecond))
	ack := 0
	for _, a := range aa {
		if a.Kind == Send {
			p, _ := Decode(a.Data)
			if p.Type == ACK2 {
				ack++
			}
		}
	}
	if got := m.Counters().ACKSent - before; got != uint64(ack) {
		t.Fatalf("ACK delta=%d wire=%d", got, ack)
	}
	s = m.Stats(core.Instant(31 * time.Millisecond))
	if s.RXBytes != 2 || s.RXAge != 10*time.Millisecond || s.TXBytes != 2000 || s.TXAge != 30*time.Millisecond {
		t.Fatalf("retained snapshot=%+v", s)
	}
	m.Step(Event{Kind: Read, Count: 1}, core.Instant(32*time.Millisecond))
	s = m.Stats(core.Instant(41 * time.Millisecond))
	if s.RXBytes != 1 || s.RXAge != 20*time.Millisecond {
		t.Fatalf("partial RX snapshot=%+v", s)
	}
}

func TestTelemetryByteParity(t *testing.T) {
	off := newMachine(t, nil)
	on := newMachine(t, func(c *Config) { c.Telemetry = true })
	ready(t, off)
	ready(t, on)
	events := []Event{incoming(Data, 3, "d"), incoming(Data, 0, "a"), incoming(Data, 1, "b"), incoming(Data, 3, "d"), incoming(Data, 2, "c"), {Kind: Read, Count: 3}, {Kind: Write, Data: []byte("opaque")}, incoming(ACK2, 0, ""), {Kind: Close}}
	for i, e := range events {
		now := core.Instant(time.Duration(i+1) * 10 * time.Millisecond)
		a, b := off.Step(e, now), on.Step(e, now)
		if len(a) != len(b) {
			t.Fatalf("event=%d effects=%d/%d", i, len(a), len(b))
		}
		for j := range a {
			if a[j].Kind != b[j].Kind || a[j].At != b[j].At || !bytes.Equal(a[j].Data, b[j].Data) {
				t.Fatalf("event=%d action=%d mismatch off=%+v on=%+v", i, j, a[j], b[j])
			}
		}
	}
}
