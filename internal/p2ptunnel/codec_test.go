package p2ptunnel

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"github.com/go-json-experiment/json"
	"os"
	"reflect"
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
func evidence(t testing.TB) []byte {
	t.Helper()
	b, e := os.ReadFile("testdata/layer-example.json")
	if e != nil {
		t.Fatal(e)
	}
	var f map[string]any
	if e = json.Unmarshal(b, &f, json.MatchCaseInsensitiveNames(true)); e != nil {
		t.Fatal(e)
	}
	return unhex(t, f["tunnel_header"].(string)+f["tcp_hex"].(string))
}
func seeds(t testing.TB) [][]byte {
	// Literal bytes plus synthetic field-table fixtures (port 4700=0x125c,
	// version 4.3.8.0, loopback, service "x").
	return [][]byte{
		evidence(t), unhex(t, "070008000008030400000000"), unhex(t, "07000000"),
		unhex(t, "080008000000000001000000"), unhex(t, "09000800ffffffff01000100"),
		unhex(t, "010710000200125c7f0000010000000000000000"),
		unhex(t, "010734000200125c7f0000010000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"),
		unhex(t, "03070000"), unhex(t, "0407010001"), unhex(t, "04070000"),
		unhex(t, "05070000"), unhex(t, "06070000"), unhex(t, "07070000"),
		unhex(t, "0c00060001005c120000"), unhex(t, "0d00060001005c120100"),
		unhex(t, "0e002400010078000000000000000000000000000000000000000000000000000000000000000000"),
		unhex(t, "0f002400010078000000000000000000000000000000000000000000000000000000000000000001"),
	}
}
func seedPhase(i int) Phase {
	if i == 1 || i == 2 {
		return LoginRequest
	}
	if i == 3 || i == 4 {
		return LoginResponse
	}
	return Established
}
func TestWireFixtures(t *testing.T) {
	for i, b := range seeds(t) {
		phase := seedPhase(i)
		v, e := Decode(b, phase)
		if e != nil {
			t.Fatalf("fixture %d length %d: %v", i, len(b), e)
		}
		out, e := v.Encode(phase)
		if e != nil || !bytes.Equal(out, b) {
			t.Fatalf("fixture %d round trip", i)
		}
		for j := 0; j < len(b); j++ {
			if _, e := Decode(b[:j], phase); e == nil {
				t.Fatalf("fixture %d accepted truncation %d", i, j)
			}
		}
		if _, e := Decode(append(bytes.Clone(b), 0), phase); e == nil {
			t.Fatal("surplus accepted")
		}
	}
	b, _ := (Login{Version: AgentVersion}).Encode()
	if !bytes.Equal(b, seeds(t)[1][4:]) {
		t.Fatal("login endian")
	}
	b, _ = (Setup{Port: 4700, Address: [4]byte{127, 0, 0, 1}, Legacy: true}).Encode()
	if !bytes.Equal(b, seeds(t)[5][4:]) {
		t.Fatal("sockaddr endian")
	}
	if !bytes.Equal((PortCheck{ID: 1, Port: 4700}).Encode(), seeds(t)[13][4:]) {
		t.Fatal("verify endian")
	}
}
func TestTypedBodies(t *testing.T) {
	for _, v := range []Login{{Empty: true}, {Version: AgentVersion}, {Version: ServerVersion, Auth: make([]byte, MaxAuth)}} {
		b, e := v.Encode()
		if e != nil {
			t.Fatal(e)
		}
		out, e := ParseLogin(b)
		if e != nil || out.Version != v.Version || !bytes.Equal(out.Auth, v.Auth) || out.Empty != v.Empty {
			t.Fatal("login", e)
		}
	}
	// Service verification is decoded only in a full eight-byte result.
	// These are the payloads used by the login accumulation/capability tests.
	for _, tc := range []struct {
		payload string
		service byte
	}{
		{"", 0}, {"ff", 0}, {"ffff", 0}, {"ffffff", 0},
		{"ffffffff", 0}, {"ffffffff01", 0}, {"ffffffff0101", 0},
		{"ffffffff010101", 0}, {"ffffffff02000300", 3},
		{"ffffffff02000300aa", 3}, {"0000000000ff0000", 0},
		{"0000000002000300", 3}, {"0000000002ff03", 0},
	} {
		out, err := ParseLoginResult(unhex(t, tc.payload))
		if err != nil || out.ServiceVerify != tc.service {
			t.Fatalf("payload %s: service=%d err=%v, want %d", tc.payload, out.ServiceVerify, err, tc.service)
		}
	}
	r := LoginResult{-17, 2, 3, 4, 5}
	out, e := ParseLoginResult(r.Encode())
	if e != nil || out != r {
		t.Fatal("result")
	}
	for _, s := range []Setup{{Port: 4700, Address: [4]byte{127, 0, 0, 1}}, {Legacy: true, Port: 80}, {Service: "01234567890123456789012345678901"}} {
		b, e := s.Encode()
		if e != nil {
			t.Fatal(e)
		}
		out, e := ParseSetup(b)
		if e != nil || out != s {
			t.Fatal("setup", e)
		}
	}
	s := ServiceCheck{23, "01234567890123456789012345678901", 1}
	b, e := s.Encode()
	if e != nil {
		t.Fatal(e)
	}
	sc, e := ParseService(b)
	if e != nil || sc != s {
		t.Fatal("service")
	}
}
func TestBadFields(t *testing.T) {
	bad := []Frame{{DataCommand, 1, nil}, {DataCommand, 1, make([]byte, 8193)}, {EndCommand, 1, []byte{0}}, {CloseCommand, 1, []byte{0, 0}}, {SetupCommand, 1, make([]byte, 51)}, {PortRequest, 1, make([]byte, 6)}, {ServiceRequest, 0, bytes.Repeat([]byte{1}, 36)}, {Command(10), 0, nil}}
	for _, v := range bad {
		if _, e := v.Encode(Established); e == nil {
			t.Fatal("bad frame accepted", v.Command)
		}
	}
	if _, e := (Login{Auth: make([]byte, 1001)}).Encode(); e == nil {
		t.Fatal("auth limit")
	}
	if _, e := ParseLogin(unhex(t, "00080304ffffffff")); e == nil {
		t.Fatal("auth length")
	}
	if _, e := (Setup{Service: "a\x00b"}).Encode(); e == nil {
		t.Fatal("embedded nul")
	}
	if _, e := Decode(unhex(t, "07000000"), LoginResponse); e == nil {
		t.Fatal("wrong phase")
	}
	d := Decoder{}
	if _, _, e := d.Feed(unhex(t, "0201ffff"), Established); e == nil {
		t.Fatal("oversize header")
	}
	if d.Buffered() != 4 {
		t.Fatal("allocated body")
	}
	if _, _, e := d.Feed(nil, Established); e == nil {
		t.Fatal("poison not retained")
	}
	d.Reset()
}
func TestStreamSplitsAndCoalescing(t *testing.T) {
	b := evidence(t)
	for split := 0; split <= len(b); split++ {
		d := Decoder{}
		var got []Frame
		for _, part := range [][]byte{b[:split], append(bytes.Clone(b[split:]), b...)} {
			for len(part) > 0 {
				n, f, e := d.Feed(part, Established)
				if e != nil || n == 0 {
					t.Fatal(e)
				}
				part = part[n:]
				if f != nil {
					got = append(got, *f)
				}
			}
		}
		if len(got) != 2 || d.Buffered() != 0 {
			t.Fatal("lost framing", split)
		}
	}
}
func FuzzDecode(f *testing.F) {
	for i, b := range seeds(f) {
		f.Add(b, byte(seedPhase(i)))
	}
	for _, seed := range compatibilitySeeds() {
		f.Add(unhex(f, seed.wire), byte(seed.phase))
	}
	f.Fuzz(func(t *testing.T, b []byte, p byte) {
		phase := Phase(p % 3)
		if v, e := Decode(b, phase); e == nil {
			out, e := v.Encode(phase)
			if e == nil && !bytes.Equal(out, b) {
				t.Fatal("sendable frame round trip")
			}
			if len(v.Payload) > MaxData || len(b) != HeaderSize+int(binary.LittleEndian.Uint16(b[2:])) || v.Command != Command(b[0]) || v.Channel != b[1] || !bytes.Equal(v.Payload, b[4:]) {
				t.Fatal("receive envelope")
			}
		}
		if v, e := ParseLogin(b); e == nil {
			out, e := v.Encode()
			if e != nil || !bytes.Equal(out, b) {
				t.Fatal("login round trip")
			}
		}
		if v, e := ParseLoginResult(b); e == nil && (len(b) == 8 && !bytes.Equal(v.Encode(), b) || len(b) == 4 && !bytes.Equal(v.Encode()[:4], b)) {
			t.Fatal("result round trip")
		}
		if v, e := ParsePort(b); e == nil {
			canonical := v.Encode()
			v2, err := ParsePort(canonical)
			if err != nil || v2.ID != v.ID || v2.Port != v.Port || (v2.Valid != 0) != (v.Valid != 0) || v2.Valid > 1 || v2.Reserved != 0 {
				t.Fatal("port semantics/canonical send")
			}
		}
		if v, e := ParseSetup(b); e == nil {
			out, e := v.Encode()
			if e != nil {
				t.Fatal(e)
			}
			v2, e := ParseSetup(out)
			if e != nil || v2 != v {
				t.Fatal("setup semantics")
			}
		}
		if v, e := ParseService(b); e == nil {
			out, e := v.Encode()
			if e != nil {
				t.Fatal(e)
			}
			v2, e := ParseService(out)
			if e != nil || v2.ID != v.ID || v2.Service != v.Service || (v2.Valid != 0) != (v.Valid != 0) || v2.Valid > 1 {
				t.Fatal("service semantics")
			}
		}
	})
}
func FuzzStream(f *testing.F) {
	for i, b := range seeds(f) {
		f.Add(b, byte(seedPhase(i)), byte(1))
	}
	for _, seed := range compatibilitySeeds() {
		f.Add(unhex(f, seed.wire), byte(seed.phase), byte(1))
	}
	f.Fuzz(func(t *testing.T, b []byte, p, chunk byte) {
		var d Decoder
		phase := Phase(p % 3)
		offset := 0
		frameStart := 0
		for offset < len(b) {
			end := min(len(b), offset+1+int(chunk))
			n, v, e := d.Feed(b[offset:end], phase)
			offset += n
			if d.Buffered() > MaxInput {
				t.Fatal("parser bound")
			}
			if e != nil {
				return
			}
			if n == 0 {
				t.Fatal("no progress")
			}
			if v != nil {
				direct, e := Decode(b[frameStart:offset], phase)
				if e != nil || !reflect.DeepEqual(direct, *v) {
					t.Fatal("stream differs from complete receive frame")
				}
				frameStart = offset
			}
		}
	})
}
