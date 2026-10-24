package iotc

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"os"
	"reflect"
	"testing"
)

type metamorphicFixture struct {
	Name, Mode, Plain, Wire, Key, IV, VariantPlain, VariantWire string
	Tolerated                                                   []string `json:",omitempty"`
}

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
	return rows
}
func metamorphicPeer(t *testing.T, row metamorphicFixture, tr Transport, observers ...func(Event, []Action)) (*matrixPeer, Link) {
	var observe func(Event, []Action)
	if len(observers) > 0 {
		observe = observers[0]
	}
	t.Helper()
	observer := observe
	observe = func(e Event, actions []Action) {
		// Cover every outgoing legacy frame in the owner exchange, including
		// headers encrypted inside modern records, rather than only a builder.
		for _, a := range actions {
			var p Packet
			var err error
			switch {
			case a.Kind == Seal && len(a.Data) >= 16 && le16(a.Data) == 0x0204:
				plain := a.Data
				if a.Mode == RawMasterRSA {
					// The RSA input pads the complete inner frame to 256 bytes.
					n := 16 + int(le16(plain[4:]))
					if n > len(plain) {
						t.Fatal("short RSA inner frame")
					}
					plain = plain[:n]
				}
				p, err = ParsePacket(plain)
			case a.Kind == Send && len(a.Data) >= 16 && le32(a.Data) != 0xcc51:
				p, err = DecodeLegacy(a.Data, ScrambleKey([16]byte{}))
			default:
				continue
			}
			if err != nil || p.Revision != 29 || p.Flags&0xf4 != 0 {
				t.Fatalf("sent noncanonical legacy header: revision=%d flags=%02x err=%v", p.Revision, p.Flags, err)
			}
		}
		if observer != nil {
			observer(e, actions)
		}
	}
	if row.Mode == "peer" {
		if row.Name == "hello" || row.Name == "alternate-hello" {
			m, l := layoutHelloMachine(t)
			return &matrixPeer{observe: observe, t: t, h: &harness{m: m}}, l
		}
		m := established(t, row.Name[:6] == "direct")
		m.alive = 17
		if !m.direct {
			m.path.Transport = tr
		}
		return &matrixPeer{observe: observe, t: t, h: &harness{m: m}}, m.path
	}
	if row.Name == "100c" {
		h := newHarness(t, "silent", 0)
		p := &matrixPeer{observe: observe, t: t, h: h, transport: tr, sent: map[uint16]int{}}
		p.run(p.step(Event{Kind: Start}))
		for _, v := range h.m.endpoints[:h.m.count] {
			if v.master && v.connected && v.link.Transport == tr {
				p.master = v.link
				return p, v.link
			}
		}
		t.Fatal("master not started")
	}
	p := newMatrixPeerObserved(t, 1, "same-fields", tr, observe)
	if row.Mode == "alert" {
		p.rendezvous()
		return p, p.server
	}
	op := le16(unhex(row.Plain)[8:])
	p.prepare(op)
	if row.Mode == "knock" {
		return p, p.peerLink()
	}
	return p, p.server
}
func metamorphicRecord(p *matrixPeer, row metamorphicFixture, plain []byte) []byte {
	if row.Mode == "peer" || row.Mode == "knock" {
		return Transform(plain, p.h.m.key, plain[3]&1 != 0, false)
	}
	if row.Mode != "alert" {
		return p.record(le16(plain[8:]), plain)
	}
	aad := unhex("51cc0000020e000007000000")
	put16(aad[6:], uint16(len(plain)+16))
	iv := p.h.ivs[3]
	return append(aad, p.h.keys[3].Seal(nil, iv[:], plain, aad)...)
}

// Only Open's cryptographic input differs by construction. Every subsequent
// action (including canonical Seal plaintext/AAD and actual peer sends) is kept.
func metamorphicIngress(t *testing.T, p *matrixPeer, l Link, wire []byte) []Action {
	t.Helper()
	queue := p.step(Event{Kind: Wire, Link: l, Data: wire, Marker: Marker{100, 123}})
	var out []Action
	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		if a.Kind == Open {
			queue = append(queue, p.step(p.open(a))...)
			continue
		}
		out = append(out, a)
	}
	return out
}
func metamorphicMeaning(t *testing.T, row metamorphicFixture, b []byte) any {
	t.Helper()
	if row.Mode == "alert" {
		a, err := ParseAlert(b)
		if err != nil {
			t.Fatal(err)
		}
		a.Trigger = 0
		return a
	}
	p, err := ParsePacket(b)
	if err != nil {
		t.Fatal(err)
	}
	if row.Mode == "peer" {
		switch row.Name {
		case "hello", "alternate-hello":
			a, err := ParseAddress(p.Body[:16])
			if err != nil {
				t.Fatal(err)
			}
			return struct {
				Address string
				Echo    string
			}{a.String(), hex.EncodeToString(p.Body[16:22])}
		case "direct-data", "relay-data":
			rid, data, err := ParseData(p)
			if err != nil {
				t.Fatal(err)
			}
			return struct {
				RID  [8]byte
				Data string
			}{rid, string(data)}
		default:
			return [8]byte(p.Body)
		}
	}
	c, err := ParseControl(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// All changed offsets are explicit opaque/reserved receive fields, not guesses
// from zero-filled sender fixtures.
func metamorphicMutate(row metamorphicFixture, noise []byte) []byte {
	b := bytes.Clone(unhex(row.Plain))
	fill := byte(0xa5)
	if len(noise) > 0 {
		fill = noise[0]
	}
	set := func(lo, hi int) {
		for i := lo; i < hi; i++ {
			b[i] = fill
		}
	}
	addr := func(off int) { set(off, off+2); set(off+8, off+16) }
	if row.Mode != "alert" {
		// Receive revision, endian marker and unassigned flag bits do not
		// change consumed meaning. Bit 0 still selects the scrambling mode.
		b[2] = fill
		b[3] = b[3]&9 | fill&0xf6
	}
	switch row.Mode {
	case "alert":
		b[7] = fill
		b = append(b, noise...)
		return b
	case "knock":
		op := le16(b[8:])
		start := 44
		if op == 0x0405 {
			start = 45
		}
		if op == 0x0406 {
			start = 48
		}
		b = append(b[:start], noise...)
	case "modern":
		switch le16(b[8:]) {
		case 0x100c:
			set(18, 20)
			set(40, 56)
			set(72, 74)
			addr(56)
			addr(90)
			b[77] = fill
			b[87] = fill
			// Insert an unknown unit before known ones, so an incorrect skip offset
			// cannot pass by merely discarding the end of the message.
			unit := []byte{0xfe, fill, 0, 0}
			put16(unit[2:], uint16(len(noise)))
			tail := bytes.Clone(b[76:])
			b = append(b[:76], unit...)
			b = append(b, noise...)
			b = append(b, tail...)
			put16(b[74:], 3)
			b = append(b, noise...)
		case 0x0218:
			set(18, 20)
			set(48, 60)
			set(70, 72)
		case 0x030d:
			set(18, 20)
			addr(40)
			addr(56)
			addr(136)
			set(264, 312)
		case 0x030e:
			addr(40)
			addr(56)
			set(101, 104) // NAT consumers retain only low byte.
		case 0x010d:
			set(18, 20)
			set(50, 52)
		case 0x0129, 0x012b:
			set(18, 20)
		}
	case "peer":
		switch row.Name {
		case "direct-data", "relay-data":
			b[15] |= fill & 0xfc
		case "hello", "alternate-hello":
			addr(16)
			b = append(b, noise...)
		case "relay-alive":
			b = append(b, noise...)
		}
	}
	put16(b[4:], uint16(len(b)-16))
	return b
}
func checkMetamorphicReceive(t *testing.T, row metamorphicFixture, noise []byte, tr Transport) {
	t.Helper()
	base := unhex(row.Plain)
	changed := metamorphicMutate(row, noise)
	if !reflect.DeepEqual(metamorphicMeaning(t, row, base), metamorphicMeaning(t, row, changed)) {
		t.Fatal("semantic decode differs")
	}
	p0, l0 := metamorphicPeer(t, row, tr)
	p1, l1 := metamorphicPeer(t, row, tr)
	if row.Mode == "modern" || row.Mode == "alert" {
		// Unknown bounded envelopes have no crypto, identity or liveness effect.
		unknown := append(unhex("51cc00000263000007000000"), noise...)
		put16(unknown[6:], uint16(len(noise)))
		before := *p1.h.m
		a := metamorphicIngress(t, p1, l1, unknown)
		before.discards = p1.h.m.discards
		if !metamorphicOnlyTimer(a) || !reflect.DeepEqual(before, *p1.h.m) {
			t.Fatal("unknown envelope changed state/actions")
		}
	}
	a0 := metamorphicIngress(t, p0, l0, metamorphicRecord(p0, row, base))
	a1 := metamorphicIngress(t, p1, l1, metamorphicRecord(p1, row, changed))
	state0, state1 := *p0.h.m, *p1.h.m
	state0.discards = state1.discards
	if !reflect.DeepEqual(a0, a1) || !reflect.DeepEqual(state0, state1) {
		t.Fatalf("%s changed machine semantics/actions: %+v / %+v", row.Name, a0, a1)
	}
	if len(a0) == 0 && (row.Name == "100c" || row.Name == "0218" || row.Name == "030d" || row.Name == "020d" || row.Name == "030e" || row.Mode == "knock") {
		t.Fatal("positive fixture had no protocol action")
	}
	// Crypto safety is tested after a permissible mutation and re-authentication.
	if row.Mode == "modern" || row.Mode == "alert" {
		for _, bad := range []string{"tag", "key", "aad", "outer-sid", "server-sid", "uid", "correlation", "inner-length", "outer-length", "truncated"} {
			p, l := metamorphicPeer(t, row, tr)
			plain := bytes.Clone(changed)
			wire := metamorphicRecord(p, row, plain)
			switch bad {
			case "tag":
				wire[len(wire)-1] ^= 1
			case "key":
				ref := KeyRef(3)
				if row.Name == "100c" {
					ref = 1
				}
				block, _ := aes.NewCipher(bytes.Repeat([]byte{0xff}, 16))
				p.h.keys[ref], _ = cipher.NewGCM(block)
			case "aad":
				wire[8] ^= 1 // Deliberately fail authentication of the changed AAD.
			case "outer-sid":
				wire[8] ^= 1
				prefix, ref := 12, KeyRef(3)
				if wire[5] == 4 {
					prefix = 16
				}
				if wire[5] == 1 {
					ref = 1
				}
				iv := p.h.ivs[ref]
				wire = append(wire[:prefix], p.h.keys[ref].Seal(nil, iv[:], plain, wire[:prefix])...)
			case "server-sid":
				if row.Mode == "alert" {
					plain[0] ^= 1
				} else if wire[5] == 4 {
					wire[12] ^= 1
					iv := p.h.ivs[3]
					wire = append(wire[:16], p.h.keys[3].Seal(nil, iv[:], plain, wire[:16])...)
				} else {
					continue
				}
				if row.Mode == "alert" {
					wire = metamorphicRecord(p, row, plain)
				}
			case "uid":
				if row.Mode == "alert" {
					continue
				}
				off := 16
				if row.Name == "100c" || row.Name == "030d" || row.Name == "010d" || row.Name == "0129" || row.Name == "012b" {
					off = 20
				}
				if row.Name == "0218" {
					off = 24
				}
				plain[off] ^= 1
				wire = metamorphicRecord(p, row, plain)
			case "correlation":
				off := 16
				switch row.Name {
				case "020d":
					off = 40
				case "030e":
					off = 72
				case "alert":
					off = 4
				}
				plain[off] ^= 1
				wire = metamorphicRecord(p, row, plain)
			case "inner-length":
				if row.Mode == "alert" {
					plain = plain[:8]
				} else {
					plain[4]++
				}
				wire = metamorphicRecord(p, row, plain)
			case "outer-length":
				wire[6]++
			case "truncated":
				wire = wire[:len(wire)-1]
			}
			before := *p.h.m
			a := metamorphicIngress(t, p, l, wire)
			after := *p.h.m
			before.discards = after.discards
			before.serial = after.serial
			before.discards = after.discards
			if !metamorphicOnlyTimer(a) || !reflect.DeepEqual(before, after) {
				t.Fatalf("%s/%s unsafe mutation acted: %+v", row.Name, bad, a)
			}
		}
	}
}
func TestMetamorphicReceive(t *testing.T) {
	for _, row := range metamorphicFixtures(t) {
		t.Run(row.Name, func(t *testing.T) {
			for _, tr := range []Transport{UDP, TCP} {
				if tr == TCP && (row.Mode == "knock" || row.Mode == "peer") {
					continue
				}
				for _, n := range []int{0, 1, 15, 64, 256} {
					checkMetamorphicReceive(t, row, bytes.Repeat([]byte{0xa5}, n), tr)
				}
			}
		})
	}
}
func FuzzMetamorphicReceive(f *testing.F) {
	rows := metamorphicFixtures(f)
	for i := range rows {
		f.Add(uint8(i), false, []byte{0xa5})
	}
	f.Fuzz(func(t *testing.T, which uint8, tcp bool, noise []byte) {
		if len(noise) > 256 {
			return
		}
		row := rows[int(which)%len(rows)]
		tr := UDP
		if tcp && row.Mode != "peer" && row.Mode != "knock" {
			tr = TCP
		}
		checkMetamorphicReceive(t, row, noise, tr)
	})
}

// Immutable transcript bytes include the entire authenticated envelope and the
// public synthetic key/IV. They are never regenerated by a test oracle.
func TestMetamorphicTranscripts(t *testing.T) {
	for _, row := range metamorphicFixtures(t) {
		t.Run(row.Name, func(t *testing.T) {
			if row.Wire == "" {
				t.Fatal("missing complete wire transcript")
			}
			if row.Mode == "modern" || row.Mode == "alert" {
				block, err := aes.NewCipher(unhex(row.Key))
				if err != nil {
					t.Fatal(err)
				}
				aead, err := cipher.NewGCM(block)
				if err != nil {
					t.Fatal(err)
				}
				wire := unhex(row.Wire)
				prefix := 12
				if wire[5] == 4 {
					prefix = 16
				}
				plain, err := aead.Open(nil, unhex(row.IV), wire[prefix:], wire[:prefix])
				if err != nil || !bytes.Equal(plain, unhex(row.Plain)) {
					t.Fatal("immutable transcript authentication differs", err)
				}
			}
			p, l := metamorphicPeer(t, row, UDP)
			if got := metamorphicRecord(p, row, unhex(row.Plain)); !bytes.Equal(got, unhex(row.Wire)) {
				t.Fatal("immutable transcript differs from context", fmt.Sprint(len(got)))
			}
			metamorphicIngress(t, p, l, unhex(row.Wire))
			if row.VariantPlain == "" || row.VariantWire == "" {
				t.Fatal("missing immutable mutation transcript")
			}
			p, l = metamorphicPeer(t, row, UDP)
			variant := metamorphicMutate(row, bytes.Repeat([]byte{0xa5}, 15))
			if !bytes.Equal(variant, unhex(row.VariantPlain)) || !bytes.Equal(metamorphicRecord(p, row, variant), unhex(row.VariantWire)) {
				t.Fatal("immutable mutation transcript differs")
			}
			metamorphicIngress(t, p, l, unhex(row.VariantWire))
		})
	}
}

func metamorphicOnlyTimer(actions []Action) bool {
	for _, a := range actions {
		if a.Kind != ArmTimer {
			return false
		}
	}
	return true
}
