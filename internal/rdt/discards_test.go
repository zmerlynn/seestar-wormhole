package rdt

import "testing"

func TestRDTDiscardCounters(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule int
		drop func(*Machine)
	}{
		{"idle_receive", 87, func(m *Machine) { m.status = Idle; m.Step(Event{Kind: Receive}, 0) }},
		{"early_data", 87, func(m *Machine) { m.status = Creating; m.receive(Packet{Type: Data, Payload: []byte("x")}) }},
		{"invalid_fin", 87, func(m *Machine) { m.receive(Packet{Type: FIN}) }},
		{"coalesced_information", 88, func(m *Machine) {
			m.cfg.SendCapacity = 1
			m.sends = 1
			m.send(Packet{Type: DataACK})
			m.send(Packet{Type: DataACK, Sequence: Sequence{Low: 1}})
		}},
		{"invalid_packet", 89, func(m *Machine) { m.receive(Packet{Type: Data}) }},
		{"unknown_type", 90, func(m *Machine) { m.receive(Packet{Type: 255}) }},
		{"wrong_id", 91, func(m *Machine) { m.receive(Packet{Type: Data, ID: 255, Payload: []byte("x")}) }},
		{"zero_heartbeat", 91, func(m *Machine) { m.receive(Packet{Type: Heartbeat, Version: 5}) }},
		{"sequence_limit", 92, func(m *Machine) {
			m.data(Packet{Type: Data, Sequence: Sequence{Low: 0xf0000001}, Payload: []byte("x")})
		}},
		{"duplicate", 93, func(m *Machine) {
			p := Packet{Type: Data, Payload: []byte("x")}
			m.data(p)
			m.data(p)
			if m.rxBytes != 1 {
				t.Fatal("duplicate delivered")
			}
		}},
		{"receive_capacity", 94, func(m *Machine) { m.cfg.RXBytes = 1; m.data(Packet{Type: Data, Payload: []byte("xx")}) }},
		{"valid_ack_then_future", 95, func(m *Machine) {
			m.tx = []txPacket{{sent: true, packet: Packet{Type: Data, Payload: []byte("x")}}}
			m.txBytes = 1
			m.acknowledged(Packet{Type: DataACK})
			if m.Discards()[8] != 0 || m.txBytes != 0 {
				t.Fatal("valid ACK counted or not applied")
			}
			m.acknowledged(Packet{Type: DataACK, Sequence: Sequence{Low: 1}})
		}},
		{"valid_sack_then_future", 95, func(m *Machine) {
			seq := Sequence{Low: 1}
			m.tx = []txPacket{{sent: true, packet: Packet{Type: Data, Sequence: seq, Payload: []byte("x")}}}
			m.txBytes = 1
			payload, _ := (SACK{Count: 1, Entries: []Range{{seq, seq}}}).Encode()
			m.acknowledged(Packet{Type: SACK2, Payload: payload})
			if m.Discards()[8] != 0 || m.txBytes != 0 {
				t.Fatal("valid SACK counted or not applied")
			}
			m.acknowledged(Packet{Type: DataACK, Sequence: Sequence{Low: 2}})
		}},
		{"future_ack", 95, func(m *Machine) { m.acknowledged(Packet{Type: DataACK, Sequence: Sequence{Low: 100}}) }},
		{"unsent_ack", 95, func(m *Machine) {
			m.tx = []txPacket{{packet: Packet{Type: Data, Payload: []byte("x")}}}
			m.txBytes = 1
			m.acknowledged(Packet{Type: DataACK})
			if m.txBytes != 1 {
				t.Fatal("unsent data freed")
			}
		}},
		{"ambiguous_sample", 95, func(m *Machine) { m.sample(txPacket{retried: true}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMachine(t, nil)
			m.status = Established
			m.peerVersion = 4
			tc.drop(m)
			got := m.Discards()
			if got[tc.rule-87] != 1 {
				t.Fatalf("R%03d count = %d, want 1 (all %v)", tc.rule, got[tc.rule-87], got)
			}
			for i, n := range got {
				if i != tc.rule-87 && n != 0 {
					t.Fatalf("unexpected R%03d count %d", 87+i, n)
				}
			}
		})
	}
}

func TestRDTControlInformation(t *testing.T) {
	sack := func(ranges ...Range) Packet {
		b, _ := (SACK{Count: uint32(len(ranges)), Entries: ranges}).Encode()
		return Packet{Type: SACK2, Payload: b}
	}
	seq := func(n uint32) Sequence { return Sequence{Low: n} }
	for _, tc := range []struct {
		name      string
		old, next Packet
		want      uint64
	}{
		{"identical_exact", Packet{Type: DataACK}, Packet{Type: DataACK}, 0},
		{"lost_exact", Packet{Type: DataACK}, Packet{Type: DataACK, Sequence: seq(1)}, 1},
		{"cumulative_covers", Packet{Type: ACK2, Sequence: seq(1)}, Packet{Type: ACK2, Sequence: seq(2)}, 0},
		{"cumulative_loses", Packet{Type: ACK2, Sequence: seq(2)}, Packet{Type: ACK2, Sequence: seq(1)}, 1},
		{"sack_covers", sack(Range{seq(1), seq(4)}), sack(Range{seq(1), seq(2)}, Range{seq(3), seq(4)}), 0},
		{"sack_covers_unsorted", sack(Range{seq(1), seq(4)}), sack(Range{seq(3), seq(4)}, Range{seq(1), seq(2)}), 0},
		{"sack_loses", sack(Range{seq(1), seq(4)}), sack(Range{seq(1), seq(2)}, Range{seq(4), seq(4)}), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMachine(t, func(c *Config) { c.SendCapacity = 1 })
			m.sends = 1
			m.send(tc.old)
			m.send(tc.next)
			if n := m.Discards()[1]; n != tc.want {
				t.Fatalf("R088 count=%d, want %d", n, tc.want)
			}
		})
	}
}
