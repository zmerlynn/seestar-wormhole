package rdt

import (
	"encoding/binary"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

const auditUnknown = "5a97c2f144050000000000000000000000070000"

func TestC14UnknownThenData(t *testing.T) {
	p, err := Decode(unhex(t, auditUnknown))
	if err != nil {
		t.Fatalf("C14 literal decode: %v", err)
	}
	if p.Type != 0x44 {
		t.Fatalf("type=%x; want 44", p.Type)
	}
	if _, err := p.Encode(); err == nil {
		t.Fatal("unknown type emitted")
	}
	m := newMachine(t, nil)
	ready(t, m)
	before, watchdog, heartbeat := m.Usage(), m.watchdog, m.heartbeatAt
	a := m.Step(Event{Kind: Receive, Packet: p}, 0)
	if m.Status() != Established || m.Usage() != before || m.watchdog != watchdog || m.heartbeatAt != heartbeat || has(a, Send) {
		t.Fatalf("unknown changed state: %+v", a)
	}
	p, err = Decode(unhex(t, "5a97c2f102050300000000000000000001070000616263"))
	if err != nil {
		t.Fatal(err)
	}
	m.Step(Event{Kind: Receive, Packet: p}, 0)
	a = m.Step(Event{Kind: Read, Count: 3}, 0)
	if !has(a, Bytes) || m.Status() != Established {
		t.Fatalf("valid packet after unknown: %+v", a)
	}
	for _, x := range a {
		if x.Kind == Bytes && string(x.Data) != "abc" {
			t.Fatalf("bytes=%q", x.Data)
		}
	}
	// Unknown input cannot keep a quiet session alive.
	m = newMachine(t, nil)
	ready(t, m)
	for sec := 1; sec <= 130 && m.Status() != Stopped; sec++ {
		m.Step(Event{Kind: Receive, Packet: pUnknown(t)}, core.Instant(time.Duration(sec)*time.Second))
	}
	if m.Status() != Stopped {
		t.Fatal("unknown packets refreshed watchdog")
	}
}
func pUnknown(t testing.TB) Packet {
	p, err := Decode(unhex(t, auditUnknown))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestC14ReceiveSafety(t *testing.T) {
	for _, literal := range []string{
		"5a97c2f144050100000000000000000000070000",   // declared missing body
		"5a97c2f101050100000000000000000000070000ff", // known no-body control
		"5a97c2f102050000000000000000000000070000",   // empty DATA
	} {
		if _, err := Decode(unhex(t, literal)); err == nil {
			t.Fatalf("unsafe envelope accepted: %s", literal)
		}
	}
	b := unhex(t, auditUnknown)
	for n := 0; n < len(b); n++ {
		if _, err := Decode(b[:n]); err == nil {
			t.Fatalf("short unknown header %d accepted", n)
		}
	}
	b = append(b, make([]byte, MaxData)...)
	binary.LittleEndian.PutUint16(b[6:], MaxData)
	p, err := Decode(b)
	if err != nil || len(p.Payload) != MaxData {
		t.Fatalf("bounded unknown: len=%d err=%v", len(p.Payload), err)
	}
	b[HeaderSize] = 99
	if p.Payload[0] != 0 {
		t.Fatal("unknown payload aliases read buffer")
	}
	b = append(b, 0)
	binary.LittleEndian.PutUint16(b[6:], MaxData+1)
	if _, err := Decode(b); err == nil {
		t.Fatal("unknown receive cap lost")
	}
}
