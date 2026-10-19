package rdt

import (
	"bytes"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func newMachine(t testing.TB, edit func(*Config)) *Machine {
	t.Helper()
	c := DefaultConfig()
	c.LocalID = 7
	if edit != nil {
		edit(&c)
	}
	m, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func packets(t testing.TB, a []Action) []Packet {
	t.Helper()
	var out []Packet
	for _, v := range a {
		if v.Kind == Send {
			p, e := Decode(v.Data)
			if e != nil {
				t.Fatal(e)
			}
			out = append(out, p)
		}
	}
	return out
}
func has(a []Action, k ActionKind) bool {
	for _, v := range a {
		if v.Kind == k {
			return true
		}
	}
	return false
}
func ready(t testing.TB, m *Machine) {
	t.Helper()
	m.Step(Event{Kind: Start}, 0)
	m.Step(Event{Kind: Receive, Packet: Packet{Type: Heartbeat, Version: 5, ID: 9}}, 0)
	a := m.Step(Event{Kind: Receive, Packet: Packet{Type: HeartbeatACK, Version: 5, ID: 7}}, 0)
	if !has(a, Ready) || m.Status() != Established {
		t.Fatal("creation did not become ready", a)
	}
}
func incoming(k Type, s uint32, data string) Event {
	return Event{Kind: Receive, Packet: Packet{Type: k, Version: 5, ID: 7, Sequence: Sequence{Low: s}, Transmissions: 1, Payload: []byte(data)}}
}
func TestCreationIdentityAndLiteral(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.PreviousRemoteID = 8 })
	a := m.Step(Event{Kind: Start}, 0)
	if has(a, Ready) {
		t.Fatal("premature ready")
	}
	a = m.Step(Event{Kind: Tick}, core.Instant(100*time.Millisecond))
	var wire []byte
	for _, v := range a {
		if v.Kind == Send {
			wire = v.Data
		}
	}
	if !bytes.Equal(wire, unhex(t, "5a97c2f101050000000000000000000000070000")) {
		t.Fatalf("heartbeat=%x", wire)
	}
	m.Step(Event{Kind: Receive, Packet: Packet{Type: Heartbeat, Version: 5, ID: 8}}, core.Instant(100*time.Millisecond))
	m.Step(Event{Kind: Receive, Packet: Packet{Type: HeartbeatACK, Version: 5, ID: 6}}, core.Instant(100*time.Millisecond))
	if m.Status() != Creating {
		t.Fatal("stale IDs completed creation")
	}
	a = m.Step(Event{Kind: Receive, Packet: Packet{Type: Heartbeat, Version: 5, ID: 9, Transmissions: 3, Reserved: 0x1234}}, core.Instant(100*time.Millisecond))
	pp := packets(t, a)
	if len(pp) != 1 || pp[0].Type != HeartbeatACK || pp[0].ID != 9 || pp[0].Reserved != 0x1234 || pp[0].Transmissions != 3 {
		t.Fatal(pp)
	}
	m.Step(Event{Kind: Receive, Packet: Packet{Type: HeartbeatACK, Version: 5, ID: 7}}, core.Instant(100*time.Millisecond))
	if m.Status() != Established {
		t.Fatal("not established")
	}
	m.Step(incoming(Data, 0, "a"), core.Instant(100*time.Millisecond))
	bad := incoming(Data, 1, "b")
	bad.Packet.ID = 6
	m.Step(bad, core.Instant(100*time.Millisecond))
	if m.Usage().RXBytes != 1 {
		t.Fatal("stale DATA admitted")
	}
}
func TestOrderingCreditAndSelectiveACK(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.MaxPayload = 100; c.TXBytes = 300; c.TXPackets = 3 })
	ready(t, m)
	a := m.Step(Event{Kind: Write, Data: bytes.Repeat([]byte{'x'}, 350), Token: 42}, 0)
	if m.Usage().TXBytes != 300 || m.Credit() != 0 {
		t.Fatal(m.Usage(), m.Credit())
	}
	for _, v := range a {
		if v.Kind == Accepted && (v.Count != 300 || v.Token != 42) {
			t.Fatal(v)
		}
	}
	sack, _ := (SACK{Count: 1, Entries: []Range{{Start: Sequence{Low: 2}, End: Sequence{Low: 2}}}}).Encode()
	m.Step(Event{Kind: Receive, Packet: Packet{Type: SACK2, Version: 5, ID: 7, Sequence: Sequence{Low: 1}, Payload: sack}}, core.Instant(time.Millisecond))
	if m.Usage().TXBytes != 200 {
		t.Fatal("SACK header treated cumulatively", m.Usage())
	}
	m.Step(incoming(ACK2, 1, ""), core.Instant(2*time.Millisecond))
	if m.Usage().TXBytes != 0 || m.Credit() != 300 {
		t.Fatal(m.Usage())
	}
	m.Step(incoming(Data, 2, "c"), core.Instant(3*time.Millisecond))
	if has(m.Step(Event{Kind: Read, Count: 100}, core.Instant(3*time.Millisecond)), Bytes) {
		t.Fatal("delivered past gap")
	}
	m.Step(incoming(Data, 0, "a"), core.Instant(4*time.Millisecond))
	m.Step(incoming(Data, 2, "c"), core.Instant(5*time.Millisecond))
	m.Step(incoming(Data, 1, "b"), core.Instant(6*time.Millisecond))
	a = m.Step(Event{Kind: Read, Count: 100}, core.Instant(6*time.Millisecond))
	var got []byte
	for _, v := range a {
		if v.Kind == Bytes {
			got = append(got, v.Data...)
		}
	}
	if string(got) != "abc" || m.Usage().RXBytes != 0 {
		t.Fatalf("read=%q usage=%+v", got, m.Usage())
	}
}
func TestFINDrainAndConservativeDeadline(t *testing.T) {
	m := newMachine(t, nil)
	ready(t, m)
	m.Step(Event{Kind: Write, Data: []byte("x")}, 0)
	a := m.Step(Event{Kind: Close}, 0)
	for _, p := range packets(t, a) {
		if p.Type == FIN {
			t.Fatal("FIN passed data")
		}
	}
	a = m.Step(incoming(ACK2, 0, ""), core.Instant(time.Millisecond))
	pp := packets(t, a)
	if len(pp) != 1 || pp[0].Type != FIN || pp[0].Sequence != (Sequence{0xffffffff, 0xffffffff}) || pp[0].Transmissions != 1 {
		t.Fatal(pp)
	}
	m.Step(Event{Kind: Receive, Packet: Packet{Type: Heartbeat, Version: 5, ID: 9}}, core.Instant(4*time.Second))
	a = m.Step(Event{Kind: Tick}, core.Instant(5*time.Second))
	if m.Status() != Stopped || !has(a, Failed) || m.Usage().TXBytes != 0 {
		t.Fatal("local shutdown bound", a)
	}
}
