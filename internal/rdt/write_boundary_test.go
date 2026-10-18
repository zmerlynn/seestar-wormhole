package rdt

import (
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// These cases cover independent packet and write boundary behavior.
func requireCount(t *testing.T, actions []Action, kind ActionKind, count int, token uint64) {
	t.Helper()
	found := 0
	for _, a := range actions {
		if a.Kind == kind {
			found++
			if a.Count != count || a.Token != token {
				t.Fatalf("action kind %v: count/token=%d/%d, want %d/%d", kind, a.Count, a.Token, count, token)
			}
		}
	}
	if found != 1 {
		t.Fatalf("action kind %v occurred %d times, want once: %+v", kind, found, actions)
	}
}

func TestWriteBudgetsIndependent(t *testing.T) {
	for _, tt := range []struct {
		name                            string
		bytes, slots, accepted, packets int
	}{
		{"packet", 1000, 1, 100, 1},
		{"byte", 150, 10, 150, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t, func(c *Config) { c.MaxPayload = 100; c.TXBytes = tt.bytes; c.TXPackets = tt.slots })
			ready(t, m)
			if m.Credit() != tt.accepted {
				t.Fatalf("initial credit=%d, want %d", m.Credit(), tt.accepted)
			}
			out := m.Step(Event{Kind: Write, Data: make([]byte, 400), Token: 42}, 0)
			requireCount(t, out, Accepted, tt.accepted, 42)
			requireCount(t, out, WriteCredit, 0, 0)
			if u := m.Usage(); u.TXBytes != tt.accepted || u.TXPackets != tt.packets || m.Credit() != 0 {
				t.Fatalf("usage=%+v credit=%d; want %d bytes/%d packets, zero credit", u, m.Credit(), tt.accepted, tt.packets)
			}
			pp := packets(t, out)
			if len(pp) != tt.packets {
				t.Fatalf("sent %d packets, want %d", len(pp), tt.packets)
			}
			requireCount(t, m.Step(Event{Kind: Write, Data: []byte("blocked"), Token: 43}, 0), Accepted, 0, 43)
			out = m.Step(incoming(ACK2, uint32(tt.packets-1), ""), core.Instant(1))
			requireCount(t, out, WriteCredit, tt.accepted, 0)
			if m.Usage() != (Usage{}) || m.Credit() != tt.accepted {
				t.Fatal("ACK did not restore independent budget", m.Usage(), m.Credit())
			}
		})
	}
}

func TestWriteStepBound(t *testing.T) {
	m := newMachine(t, nil)
	ready(t, m)
	credit := m.Credit()
	out := m.Step(Event{Kind: Write, Data: make([]byte, 10000), Token: 19}, 0)
	requireCount(t, out, Accepted, 8196, 19)
	requireCount(t, out, WriteCredit, credit-8196, 0)
	if u := m.Usage(); u.TXBytes != 8196 || u.TXPackets != 7 || m.Credit() != credit-8196 {
		t.Fatalf("usage=%+v credit=%d; want 8196 bytes/7 packets, credit %d", u, m.Credit(), credit-8196)
	}
	sent := 0
	for _, p := range packets(t, out) {
		sent += len(p.Payload)
	}
	if sent != 8196 {
		t.Fatalf("sent %d bytes, want bounded prefix of 8196", sent)
	}
}
