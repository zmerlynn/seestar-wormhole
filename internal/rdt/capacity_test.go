package rdt

import (
	"bytes"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

// A missing head followed by a full device window used to abort at packet 513.
// Retry at the authenticated datagram boundary is recoverable transport loss.
func TestCapacityDeviceWindowReassembly(t *testing.T) {
	p := newPair(t, 17, sim.Link{MTU: 1300, Delay: time.Millisecond}, func(c *Config) {
		c.TXBytes = 2 << 20
		c.TXPackets = 2048
		c.RXPackets = 512
		c.RXBytes = 2 << 20
	})
	p.run(time.Second)
	p.read = false
	input := bytes.Repeat([]byte{0x17}, 1000*MaxData)
	// Retain a whole default device window in the sender; hold the first packet.
	var held []byte
	var wire [][]byte
	for offset := 0; offset < len(input); {
		n := min(8196, len(input)-offset)
		for _, a := range p.owners[0].Step(Event{Kind: Write, Data: input[offset : offset+n]}, p.clock.Now()) {
			if a.Kind == Send {
				wire = append(wire, a.Data)
			}
		}
		offset += n
	}
	held, wire = wire[0], wire[1:]
	for _, b := range wire {
		pkt, _ := Decode(b)
		p.step(1, Event{Kind: Receive, Packet: pkt}, p.clock.Now())
	}
	if p.owners[1].Status() != Established {
		t.Fatalf("device burst killed RDT: %v", p.failures)
	}
	pkt, _ := Decode(held)
	p.step(1, Event{Kind: Receive, Packet: pkt}, p.clock.Now())
	p.read = true
	p.step(1, Event{Kind: Read, Count: 8196}, p.clock.Now())
	p.run(15 * time.Second)
	if !bytes.Equal(p.output[1], input) {
		t.Fatalf("delivered %d bytes want %d intact bytes", len(p.output[1]), len(input))
	}
	// Every accepted byte must eventually drain and be acknowledged.
	if p.owners[0].Usage().TXBytes != 0 || len(p.failures) != 0 {
		t.Fatalf("retry failed: TX=%+v failures=%v", p.owners[0].Usage(), p.failures)
	}
	t.Logf("reassembled %d bytes, peak RX=%+v", len(p.output[1]), p.maxUsage[1])
}

func TestCapacityRejectDoesNotAcknowledge(t *testing.T) {
	for _, limits := range []struct{ bytes, packets int }{{4, 3}, {100, 1}} {
		m := newMachine(t, func(c *Config) { c.RXBytes, c.RXPackets = limits.bytes, limits.packets })
		ready(t, m)
		m.Step(incoming(Data, 0, "abcd"), 0)
		out := m.Step(incoming(Data, 1, "e"), core.Instant(time.Millisecond))
		if m.Status() != Established || has(out, Failed) {
			t.Fatal("capacity killed RDT")
		}
		for _, p := range packets(t, out) {
			if p.Type == SACK2 || p.Type == DataACK || p.Type == UrgentACK || p.Type == ACK2 && p.Sequence.Low >= 1 {
				t.Fatalf("ACK for rejected bytes: %+v", p)
			}
		}
		m.Step(Event{Kind: Read, Count: 8196}, core.Instant(2*time.Millisecond))
		m.Step(incoming(Data, 1, "e"), core.Instant(3*time.Millisecond))
		out = m.Step(Event{Kind: Read, Count: 8196}, core.Instant(4*time.Millisecond))
		if !has(out, Bytes) || m.Usage().RXBytes != 0 {
			t.Fatal("retransmission did not restore progress", out)
		}
	}
}

// Reserve both a slot and maximum packet bytes for the missing head. Otherwise
// selectively acknowledged later packets can occupy the entire finite queue.
func TestCapacityReservesGapHead(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.RXBytes = 2 * MaxData; c.RXPackets = 2 })
	ready(t, m)
	payload := string(bytes.Repeat([]byte{7}, MaxData))
	m.Step(incoming(Data, 1, payload), 0)
	m.Step(incoming(Data, 2, payload), 0)
	m.Step(incoming(Data, 0, payload), 0)
	out := m.Step(Event{Kind: Read, Count: 8196}, 0)
	var got int
	for _, a := range out {
		if a.Kind == Bytes {
			got += len(a.Data)
		}
	}
	if got != 2*MaxData || m.Status() != Established {
		t.Fatalf("missing head cannot progress: got %d want %d", got, 2*MaxData)
	}
}

// A readable but short/partially consumed packet is not enough space for the
// next missing full packet. Later SACKed data must leave headroom even before
// the current readable prefix is drained.
func TestCapacityReservesBehindReadablePrefix(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "short", true: "partial"}[partial], func(t *testing.T) {
			m := newMachine(t, func(c *Config) { c.RXBytes = MaxData + 100; c.RXPackets = 3 })
			ready(t, m)
			n := 100
			if partial {
				n = MaxData
			}
			m.Step(incoming(Data, 0, string(bytes.Repeat([]byte{'a'}, n))), 0)
			if partial {
				m.Step(Event{Kind: Read, Count: MaxData - 100}, 0)
			}
			payload := string(bytes.Repeat([]byte{'b'}, MaxData))
			m.Step(incoming(Data, 2, payload), 0)
			m.Step(Event{Kind: Read, Count: 8196}, 0)
			m.Step(incoming(Data, 1, payload), 0)
			out := m.Step(Event{Kind: Read, Count: 8196}, 0)
			got := 0
			for _, a := range out {
				if a.Kind == Bytes {
					got += len(a.Data)
				}
			}
			if got != MaxData || m.Status() != Established {
				t.Fatalf("readable prefix consumed head reservation: delivered=%d want=%d usage=%+v", got, MaxData, m.Usage())
			}
			m.Step(incoming(Data, 2, payload), 0)
			out = m.Step(Event{Kind: Read, Count: 8196}, 0)
			got = 0
			for _, a := range out {
				if a.Kind == Bytes {
					got += len(a.Data)
				}
			}
			if got != MaxData || m.Usage().RXBytes != 0 {
				t.Fatal("later packet did not recover after retry")
			}
		})
	}
}
