package rdt

import (
	"fmt"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// Both exact and selective ACKs force a retry of the missing packet.
func TestHoleForcesBeforeRTO(t *testing.T) {
	for _, kind := range []Type{DataACK, SACK2} {
		t.Run(fmt.Sprintf("%x", kind), func(t *testing.T) {
			m := newMachine(t, func(c *Config) { c.MaxPayload = 100 })
			ready(t, m)
			m.Step(Event{Kind: Write, Data: make([]byte, 200)}, 0)
			ack := incoming(kind, 1, "")
			if kind == SACK2 {
				ack.Packet.Sequence = Sequence{}
				ack.Packet.Payload, _ = (SACK{Count: 1, Entries: []Range{{Start: Sequence{Low: 1}, End: Sequence{Low: 1}}}}).Encode()
			}
			out := m.Step(ack, core.Instant(time.Millisecond))
			pp := packets(t, out)
			if len(pp) != 1 || pp[0].Type != Data || pp[0].Sequence != (Sequence{}) || pp[0].Transmissions != 2 || len(pp[0].Payload) != 100 {
				t.Fatal("ACK must force exactly one retry of packet zero before RTO", pp)
			}
			if u := m.Usage(); u.TXBytes != 100 || u.TXPackets != 1 {
				t.Fatal("ACK must release only packet one", u)
			}
			if pp := packets(t, m.Step(ack, core.Instant(2*time.Millisecond))); len(pp) != 0 {
				t.Fatal("repeated ACK forced the same hole twice", pp)
			}
		})
	}
}

// Backoff remains capped, and every packet in each sweep retries while
// heartbeats keep the connection alive.
func TestBackoffCapOncePerSweep(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.MaxPayload = 100 })
	ready(t, m)
	m.Step(Event{Kind: Write, Data: make([]byte, 300)}, 0)
	for i, ms := range []int{500, 1500, 3500, 7500, 15500, 23500, 31500} {
		now := core.Instant(time.Duration(ms) * time.Millisecond)
		before := m.Step(Event{Kind: Receive, Packet: Packet{Type: Heartbeat, Version: 5, ID: 9}}, now-1)
		for _, p := range packets(t, before) {
			if p.Type == Data {
				t.Fatalf("early DATA retry at %dms minus 1ns: %+v", ms, p)
			}
		}
		out := m.Step(Event{Kind: Tick}, now)
		count := 0
		for _, p := range packets(t, out) {
			if p.Type == Data {
				if p.Sequence != (Sequence{Low: uint32(count)}) || p.Transmissions != byte(i+2) {
					t.Fatalf("sweep %d retry %d: %+v", i, count, p)
				}
				count++
			}
		}
		want := min(time.Duration(1<<i)*time.Second, 8*time.Second)
		if count != 3 || m.RTO() != want || m.Status() != Established || has(out, Failed) {
			t.Fatalf("at %dms retries=%d RTO=%v status=%v; want 3, %v, Established", ms, count, m.RTO(), m.Status(), want)
		}
		if u := m.Usage(); u.TXBytes != 300 || u.TXPackets != 3 {
			t.Fatal("retry changed retained data", u)
		}
	}
}
