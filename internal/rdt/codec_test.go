package rdt

import (
	"bytes"
	"encoding/hex"
	"github.com/go-json-experiment/json"
	"os"
	"testing"
)

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, e := hex.DecodeString(s)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func fixture(t testing.TB) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/layer-example.json")
	if e != nil {
		t.Fatal(e)
	}
	var f map[string]any
	if e = json.Unmarshal(b, &f, json.MatchCaseInsensitiveNames(true)); e != nil {
		t.Fatal(e)
	}
	return unhex(t, f["rdt_header"].(string)+f["tunnel_header"].(string)+f["tcp_hex"].(string))
}
func TestEvidenceFixture(t *testing.T) {
	b := fixture(t)
	p, e := Decode(b)
	if e != nil {
		t.Fatal(e)
	}
	if p.Type != Data || p.Version != 5 || p.Sequence != (Sequence{1, 0}) || p.ID != 9 || p.Transmissions != 1 || len(p.Payload) != 47 {
		t.Fatalf("wrong fields: %+v", p)
	}
	out, e := p.Encode()
	if e != nil || !bytes.Equal(out, b) {
		t.Fatal("fixture mismatch", e)
	}
	b[20] = 99
	if p.Payload[0] != 2 {
		t.Fatal("payload aliases input")
	}
}
func TestCatalog(t *testing.T) {
	for _, kind := range []Type{Heartbeat, Data, FIN, Quit, Urgent, Abort, HeartbeatACK, DataACK, FINACK, SACK2, ACK2, UrgentACK, AbortACK, Exit, NeedDTLS} {
		t.Run(hex.EncodeToString([]byte{byte(kind)}), func(t *testing.T) {
			p := Packet{Type: kind, Version: 5, ID: 7, Reserved: 0x1234, Sequence: Sequence{9, 2}, Transmissions: 2}
			switch kind {
			case Data, Urgent:
				p.Payload = []byte("abc")
			case SACK2:
				p.Payload, _ = (SACK{Count: 1, Entries: []Range{{Sequence{3, 1}, Sequence{8, 1}}}}).Encode()
			}
			b, e := p.Encode()
			if e != nil {
				t.Fatal(e)
			}
			q, e := Decode(b)
			if e != nil {
				t.Fatal(e)
			}
			c, e := q.Encode()
			if e != nil || !bytes.Equal(b, c) {
				t.Fatal("round trip", e)
			}
			for n := 0; n < len(b); n++ {
				if _, e := Decode(b[:n]); e == nil {
					t.Fatalf("accepted prefix %d", n)
				}
			}
			if _, e := Decode(append(b, 0)); e == nil {
				t.Fatal("trailing bytes")
			}
		})
	}
}
func TestSACKFixtureAndBounds(t *testing.T) {
	// DATA_SACK2 layout: zero, count 1, inclusive [3,8], 3 unused entries.
	b := unhex(t, "000000000100000003000000010000000800000001000000"+"000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000")
	s, e := DecodeSACK(b)
	if e != nil {
		t.Fatal(e)
	}
	if s.Entries[0] != (Range{Sequence{3, 1}, Sequence{8, 1}}) {
		t.Fatal(s)
	}
	out, e := s.Encode()
	if e != nil || !bytes.Equal(out, b) {
		t.Fatal(e)
	}
	for _, count := range []byte{0, 5, 255} {
		b[4] = count
		if _, e := DecodeSACK(b); e == nil {
			t.Fatal("count", count)
		}
	}
	b[4] = 1
	b[8] = 9
	if _, e := DecodeSACK(b); e == nil {
		t.Fatal("reversed")
	}
	for count := uint32(1); count <= 4; count++ {
		s.Count = count
		if _, e := s.Encode(); e != nil {
			t.Fatal(e)
		}
	}
}
func TestLimitsAndSequence(t *testing.T) {
	p := Packet{Type: Data, Version: 5, Payload: make([]byte, MaxData)}
	if _, e := p.Encode(); e != nil {
		t.Fatal(e)
	}
	p.Payload = append(p.Payload, 0)
	if _, e := p.Encode(); e == nil {
		t.Fatal("oversize")
	}
	for _, p := range []Packet{{Type: 0x44}, {Type: Data}, {Type: FIN, Payload: []byte{0}}} {
		if _, e := p.Encode(); e == nil {
			t.Fatal("invalid packet")
		}
	}
	if (Sequence{0xf0000000, 4}).Next() != (Sequence{0, 5}) || (Sequence{0xf0000000, 0xffffffff}).Next() != (Sequence{}) {
		t.Fatal("rollover")
	}
	if !(Sequence{100, 1}).Less(Sequence{0, 2}) {
		t.Fatal("ordering")
	}
}
func FuzzDecode(f *testing.F) {
	f.Add(fixture(f))
	f.Add([]byte{})
	f.Add(unhex(f, auditUnknown))
	f.Add(unhex(f, auditCompactSACK))
	f.Add(unhex(f, auditFiveSACK))
	f.Add(unhex(f, "5a97c2f145051800000000000000000000070000"+auditCompactSACK))
	f.Add(unhex(f, "5a97c2f145055800000000000000000000070000"+auditFiveSACK))
	s, _ := (SACK{Count: 1, Entries: make([]Range, 4)}).Encode()
	f.Add(s)
	for _, op := range []Type{Heartbeat, Data, FIN, Quit, Urgent, Abort, HeartbeatACK, DataACK, FINACK, SACK2, ACK2, UrgentACK, AbortACK, Exit, NeedDTLS} {
		p := Packet{Type: op, Version: 5}
		if op == Data || op == Urgent {
			p.Payload = []byte{1}
		}
		if op == SACK2 {
			p.Payload = s
		}
		b, _ := p.Encode()
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if p, e := Decode(b); e == nil {
			c, e := p.Encode()
			if e == nil && !bytes.Equal(b, c) {
				t.Fatal("sendable packet round trip")
			}
			if e != nil && known(p.Type) && p.Type != SACK2 {
				t.Fatal("known packet is not sendable")
			}
			if len(b) > MaxPacket || p.Type != Type(b[4]) || !bytes.Equal(p.Payload, b[HeaderSize:]) {
				t.Fatal("receive envelope")
			}
		}
		if s, e := DecodeSACK(b); e == nil {
			c, e := s.Encode()
			if len(b) == SACKSize && e != nil {
				t.Fatal("normal SACK is not sendable")
			}
			if e == nil {
				q, err := DecodeSACK(c)
				if err != nil || q.Count != s.Count {
					t.Fatal("SACK send semantics")
				}
				for i := uint32(0); i < s.Count; i++ {
					if q.Entries[i] != s.Entries[i] {
						t.Fatal("SACK active range")
					}
				}
			}
			if int32(s.Count) <= 0 || int(s.Count) > (len(b)-8)/16 || len(b) > MaxData {
				t.Fatal("SACK receive capacity")
			}
		}
	})
}
