package rdt

import (
	"bytes"
	"encoding/binary"
	"testing"
)

const auditCompactSACK = "000000000100000003000000010000000800000001000000"
const auditFiveSACK = "00000000050000000100000000000000010000000000000002000000000000000200000000000000030000000000000003000000000000000400000000000000040000000000000005000000000000000500000000000000"

func TestC15SACKCapacity(t *testing.T) {
	for _, tt := range []struct {
		name, literal string
		count         uint32
		high          uint32
		first, last   uint32
	}{
		{"compact", auditCompactSACK, 1, 1, 3, 8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := unhex(t, tt.literal)
			s, err := DecodeSACK(b)
			if err != nil {
				t.Fatalf("C15 %s literal decode: %v", tt.name, err)
			}
			if s.Count != tt.count {
				t.Fatalf("count=%d; want %d", s.Count, tt.count)
			}
			m := newMachine(t, func(c *Config) { c.MaxPayload = 100 })
			ready(t, m)
			m.nextTX = Sequence{High: tt.high}
			m.Step(Event{Kind: Write, Data: bytes.Repeat([]byte{'x'}, 1000)}, 0)
			m.Step(Event{Kind: Receive, Packet: Packet{Type: SACK2, ID: 7, Sequence: Sequence{Low: 99, High: tt.high}, Payload: b}}, 0)
			for _, node := range m.tx {
				if node.packet.Sequence.Low >= tt.first && node.packet.Sequence.Low <= tt.last {
					t.Fatalf("range not acknowledged: %+v", node.packet.Sequence)
				}
			}
			if m.Usage().TXPackets != 10-int(tt.last-tt.first+1) || m.tx[0].packet.Sequence.Low != 0 {
				t.Fatalf("header watermark freed packets: %+v", m.Usage())
			}
			// An identical selective ACK must never release unsent packets or FIN.
			m = newMachine(t, func(c *Config) { c.MaxPayload = 100; c.SendCapacity = 1 })
			ready(t, m)
			m.nextTX = Sequence{High: tt.high}
			m.Step(Event{Kind: Write, Data: bytes.Repeat([]byte{'x'}, 1000)}, 0)
			before := m.Usage().TXBytes
			m.Step(Event{Kind: Receive, Packet: Packet{Type: SACK2, ID: 7, Payload: b}}, 0)
			if m.Usage().TXBytes != before {
				t.Fatal("selective ACK released unsent bytes")
			}
		})
	}
}

func TestC15ReceiveAndSendBounds(t *testing.T) {
	for _, literal := range []string{auditCompactSACK, auditFiveSACK} {
		b := unhex(t, literal)
		for n := 0; n < len(b); n++ {
			if _, err := DecodeSACK(b[:n]); err == nil {
				t.Fatalf("accepted incomplete capacity at %d", n)
			}
		}
		if _, err := (Packet{Type: SACK2, Payload: b}).Encode(); err == nil {
			t.Fatal("noncanonical SACK emitted")
		}
		// Declared signed count cannot use absent slots, overflow, or be nonpositive.
		for _, count := range []uint32{0, 0x80000000, 0xffffffff, uint32((len(b)-8)/16 + 1)} {
			bad := bytes.Clone(b)
			binary.LittleEndian.PutUint32(bad[4:], count)
			if _, err := DecodeSACK(bad); err == nil {
				t.Fatalf("count %x accepted", count)
			}
		}
	}
	b := unhex(t, auditCompactSACK)
	b = append(b, 0xaa) // Unconsumed tail after complete active ranges is harmless.
	if _, err := DecodeSACK(b); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSACK(append(b, make([]byte, MaxData)...)); err == nil {
		t.Fatal("SACK receive cap lost")
	}
	m := newMachine(t, nil)
	ready(t, m)
	m.Step(Event{Kind: Close}, 0)
	m.Step(Event{Kind: Receive, Packet: Packet{Type: SACK2, ID: 7, Sequence: Sequence{0xffffffff, 0xffffffff}, Payload: unhex(t, auditFiveSACK)}}, 0)
	if m.Status() != Closing || len(m.tx) != 1 || m.tx[0].packet.Type != FIN {
		t.Fatal("SACK retired FIN")
	}
}
