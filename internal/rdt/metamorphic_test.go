package rdt

import (
	"bytes"
	"encoding/binary"
	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/wiretest"
	"os"
	"reflect"
	"testing"
	"time"
)

type metamorphicFixture struct{ Name, Wire, DTLS string }

func metamorphicFixtures(t testing.TB) []metamorphicFixture {
	t.Helper()
	b, err := os.ReadFile("testdata/metamorphic.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []metamorphicFixture
	if err = json.Unmarshal(b, &rows, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 15 {
		t.Fatal("incomplete message catalog")
	}
	return rows
}

// Reserved bytes are copied by ACK builders,
// not interpreted. SACK consumes only its active ranges, bounded by capacity.
func metamorphicPacket(t *testing.T, wire []byte) Packet {
	t.Helper()
	p, err := Decode(wire)
	if err != nil {
		t.Fatal(err)
	}
	p.Reserved = 0
	if p.Type == SACK2 {
		s, err := DecodeSACK(p.Payload)
		if err != nil {
			t.Fatal(err)
		}
		// Count and active inclusive ranges, excluding unused word/spare capacity.
		p.Payload = bytes.Clone(p.Payload[4 : 8+16*int(s.Count)])
	}
	return p
}
func metamorphicActions(t *testing.T, in []Action, reserved uint16) []Action {
	t.Helper()
	var out []Action
	for _, a := range in {
		if a.Kind == ReceiveOddity {
			if !reflect.DeepEqual(a, Action{Kind: ReceiveOddity, Type: a.Type}) {
				t.Fatal("diagnostic leaked fields")
			}
			continue
		}
		if a.Kind == Send {
			a.Data = bytes.Clone(a.Data)
			p, err := Decode(a.Data)
			if err != nil {
				t.Fatal(err)
			}
			switch p.Type {
			case HeartbeatACK, DataACK, UrgentACK, FINACK, AbortACK, ACK2, SACK2:
				if p.Reserved != reserved {
					t.Fatalf("ACK reserved=%x want copied %x", p.Reserved, reserved)
				}
			default:
				if p.Reserved != 0 {
					t.Fatal("fresh sender reserved bytes are not zero")
				}
			}
			clear(a.Data[18:20])
		}
		out = append(out, a)
	}
	return out
}
func metamorphicRun(t *testing.T, wire, unknown []byte, observers ...func(Event, core.Instant, []Action)) (Machine, []Action) {
	t.Helper()
	protected := wiretest.Seal(t, wire, 2)
	plain, err := wiretest.Open(protected, wiretest.Key())
	if err != nil {
		t.Fatal(err)
	}
	p, err := Decode(plain)
	if err != nil {
		t.Fatal(err)
	}
	m := newMachine(t, nil)
	step := func(e Event, now core.Instant) []Action {
		a := m.Step(e, now)
		if len(observers) > 0 {
			observers[0](e, now, a)
		}
		return a
	}
	step(Event{Kind: Start}, 0)
	step(Event{Kind: Receive, Packet: Packet{Type: Heartbeat, Version: 5, ID: 9}}, 0)
	step(Event{Kind: Receive, Packet: Packet{Type: HeartbeatACK, Version: 5, ID: 7}}, 0)
	if m.Status() != Established {
		t.Fatal("setup did not establish")
	}
	if p.Type == FINACK {
		step(Event{Kind: Close}, 0)
	}
	if p.Type == DataACK || p.Type == UrgentACK || p.Type == ACK2 || p.Type == SACK2 {
		step(Event{Kind: Write, Data: []byte("outstanding"), Token: 10}, 0)
	}
	// Advance the real watchdog before inserting an unknown: a spurious
	// refresh must be observable even when the following valid message refreshes it.
	step(Event{Kind: Tick}, core.Instant(time.Second))
	if unknown != nil {
		plain, err := wiretest.Open(wiretest.Seal(t, unknown, 1), wiretest.Key())
		if err != nil {
			t.Fatal(err)
		}
		q, err := Decode(plain)
		if err != nil {
			t.Fatal(err)
		}
		before := *m
		a := step(Event{Kind: Receive, Packet: q}, core.Instant(time.Second))
		if len(metamorphicActions(t, a, 0)) != 0 || !reflect.DeepEqual(before, *m) {
			t.Fatal("unknown changed protocol state/actions")
		}
	}
	var actions []Action
	for _, e := range []Event{{Kind: Receive, Packet: p}, {Kind: Tick}, {Kind: Read, Count: 8196, Token: 20}} {
		at := core.Instant(time.Second)
		if e.Kind != Receive {
			at = core.Instant(1100 * time.Millisecond)
		}
		actions = append(actions, metamorphicActions(t, step(e, at), p.Reserved)...)
	}
	// ACK templates legitimately retain the copied opaque word. All other state
	// (liveness, congestion, queues, timers) must be identical.
	m.cumulativeHeader.Reserved = 0
	m.selectiveHeader.Reserved = 0
	return *m, actions
}
func checkMetamorphicReceive(t *testing.T, row metamorphicFixture, noise []byte) {
	t.Helper()
	base := unhex(t, row.Wire)
	if row.DTLS == "" {
		t.Fatal("missing immutable authenticated transcript")
	}
	if row.DTLS != "" {
		plain, err := wiretest.Open(unhex(t, row.DTLS), wiretest.Key())
		if err != nil || !bytes.Equal(plain, base) {
			t.Fatal("retained DTLS transcript differs", err)
		}
	}
	changed := bytes.Clone(base)
	word := uint16(0xa55a)
	if len(noise) > 1 {
		word = binary.LittleEndian.Uint16(noise)
	}
	binary.LittleEndian.PutUint16(changed[18:], word)
	if Type(base[4]) == SACK2 {
		for i := 20; i < 24; i++ {
			changed[i] = byte(word)
		}
		changed = append(changed[:44], noise...)
		binary.LittleEndian.PutUint16(changed[6:], uint16(len(changed)-20))
	}
	if !reflect.DeepEqual(metamorphicPacket(t, base), metamorphicPacket(t, changed)) {
		t.Fatal("semantic decode differs")
	}
	unknown := append(unhex(t, "5a97c2f144050000123456789abcdef003feffff"), noise...)
	binary.LittleEndian.PutUint16(unknown[6:], uint16(len(noise)))
	protected := wiretest.Seal(t, changed, 3)
	bad := bytes.Clone(protected)
	bad[len(bad)-1] ^= 1
	if _, err := wiretest.Open(bad, wiretest.Key()); err == nil {
		t.Fatal("bad tag accepted")
	}
	wrong := bytes.Clone(wiretest.Key())
	wrong[0] ^= 1
	if _, err := wiretest.Open(protected, wrong); err == nil {
		t.Fatal("wrong key accepted")
	}
	m0, a0 := metamorphicRun(t, base, nil)
	m1, a1 := metamorphicRun(t, changed, unknown)
	if !reflect.DeepEqual(a0, a1) || !reflect.DeepEqual(m0, m1) {
		t.Fatalf("%s changed machine semantics/actions: %v / %v", row.Name, a0, a1)
	}
	for _, b := range [][]byte{changed[:len(changed)-1], append(bytes.Clone(changed), 0)} {
		if _, err := Decode(b); err == nil {
			t.Fatal("inexact extent accepted")
		}
	}
}
func TestMetamorphicReceive(t *testing.T) {
	for _, row := range metamorphicFixtures(t) {
		t.Run(row.Name, func(t *testing.T) {
			for _, n := range []int{0, 1, 15, 16, 48, 1256} {
				checkMetamorphicReceive(t, row, bytes.Repeat([]byte{0xff}, n))
			}
			b := unhex(t, row.Wire)
			for n := 0; n < len(b); n++ {
				if _, err := Decode(b[:n]); err == nil {
					t.Fatalf("truncation %d accepted", n)
				}
			}
			m := newMachine(t, nil)
			ready(t, m)
			before := *m
			p, err := Decode(b)
			if err != nil {
				t.Fatal(err)
			}
			p.ID = 6
			if a := m.Step(Event{Kind: Receive, Packet: p}, 0); len(a) != 0 || !reflect.DeepEqual(before, *m) {
				t.Fatal("foreign ID accepted")
			}
		})
	}
}
func FuzzMetamorphicReceive(f *testing.F) {
	rows := metamorphicFixtures(f)
	for i := range rows {
		f.Add(uint8(i), []byte{0xa5, 0x5a})
	}
	f.Fuzz(func(t *testing.T, which uint8, noise []byte) {
		if len(noise) > MaxData-24 {
			return
		}
		checkMetamorphicReceive(t, rows[int(which)%len(rows)], noise)
	})
}
