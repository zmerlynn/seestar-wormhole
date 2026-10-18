package iotc

import (
	"bytes"
	"reflect"
	"testing"
)

func TestMetamorphicLegacySafety(t *testing.T) {
	for _, row := range metamorphicFixtures(t) {
		if row.Mode != "peer" && row.Mode != "knock" {
			continue
		}
		t.Run(row.Name, func(t *testing.T) {
			for _, bad := range []string{"source", "rid", "channel", "datatype", "extension", "allocation", "flag", "refusal", "zero-result", "consumed-prefix", "extent"} {
				p, l := metamorphicPeer(t, row, UDP)
				b := metamorphicMutate(row, []byte{0xa5})
				op := le16(b[8:])
				switch bad {
				case "source":
					l.Address = p.master.Address // Invalid source cannot acquire authority.
				case "rid":
					if row.Name == "hello" || row.Name == "alternate-hello" {
						b[32] ^= 1
					} else if row.Mode == "knock" {
						b[43] ^= 1
					} else if row.Name == "direct-data" || row.Name == "relay-data" {
						b[27] ^= 1
					} else {
						b[23] ^= 1
					}
				case "channel":
					if row.Name != "direct-data" && row.Name != "relay-data" {
						continue
					}
					b[14] = 1
				case "datatype":
					if row.Name != "direct-data" && row.Name != "relay-data" {
						continue
					}
					b[15] &= 0xfc
				case "extension":
					if row.Name != "direct-data" && row.Name != "relay-data" {
						continue
					}
					b[16] = 11
				case "allocation":
					if row.Name != "relay-data" && row.Name != "relay-alive" && row.Name != "relay-close" {
						continue
					}
					b[12] ^= 1
				case "flag":
					if row.Mode == "knock" || row.Name == "hello" || row.Name == "alternate-hello" {
						continue
					}
					b[3] &^= 8
				case "refusal":
					if row.Mode != "knock" {
						continue
					}
					b[11] |= 2
				case "zero-result":
					if op != 0x0405 && op != 0x0406 {
						continue
					}
					clear(b[44:min(48, len(b))])
				case "consumed-prefix":
					n := 23
					if row.Mode == "knock" {
						n = 27
						if op == 0x0405 {
							n = 28
						}
						if op == 0x0406 {
							n = 31
						}
					} else if row.Name == "direct-data" || row.Name == "relay-data" {
						n = 11
					} else if row.Name != "hello" && row.Name != "alternate-hello" {
						n = 7
					}
					b = b[:16+n]
					put16(b[4:], uint16(n))
				case "extent":
					b[4]++
				}
				before := *p.h.m
				out := metamorphicIngress(t, p, l, metamorphicRecord(p, row, b))
				after := *p.h.m
				before.discards = after.discards
				if !metamorphicOnlyTimer(out) || !reflect.DeepEqual(before, after) {
					t.Fatalf("%s accepted unsafe %s: %+v", row.Name, bad, out)
				}
			}
		})
	}
}

func TestMetamorphicMasterExtents(t *testing.T) {
	row := metamorphicFixtures(t)[0]
	for _, bad := range []string{"count", "unit-header", "unit-extent", "server-address", "cap"} {
		p, l := metamorphicPeer(t, row, UDP)
		b := unhex(row.Plain)
		switch bad {
		case "count":
			put16(b[74:], 3)
		case "unit-header":
			b = b[:77]
		case "unit-extent":
			b[88], b[89] = 0xff, 0xff
		case "server-address":
			clear(b[92:94])
		case "cap":
			b = append(b, bytes.Repeat([]byte{0xaa}, MaxPacket)...)
		}
		put16(b[4:], uint16(len(b)-16))
		before := *p.h.m
		out := metamorphicIngress(t, p, l, metamorphicRecord(p, row, b))
		after := *p.h.m
		before.serial = after.serial
		before.discards = after.discards
		if !metamorphicOnlyTimer(out) || !reflect.DeepEqual(before, after) {
			t.Fatalf("unsafe %s master acted", bad)
		}
	}
}

func TestMetamorphicUnknownBounds(t *testing.T) {
	var row metamorphicFixture
	for _, r := range metamorphicFixtures(t) {
		if r.Name == "020d" {
			row = r
		}
	}
	for _, n := range []int{0, 1, MaxPacket - 12, MaxPacket - 11} {
		p, l := metamorphicPeer(t, row, UDP)
		wire := append(unhex("51cc00000263000007000000"), bytes.Repeat([]byte{0xa5}, n)...)
		put16(wire[6:], uint16(n))
		_, err := ParseRecord(wire)
		if (err == nil) != (n <= MaxPacket-12) {
			t.Fatalf("unknown size=%d err=%v", n, err)
		}
		before := *p.h.m
		out := metamorphicIngress(t, p, l, wire)
		before.discards = p.h.m.discards
		if !metamorphicOnlyTimer(out) || !reflect.DeepEqual(before, *p.h.m) {
			t.Fatal("unknown changed authority")
		}
	}
	// An authenticated unknown inner opcode consumes a crypto token, but cannot
	// acquire identity, liveness or relay state; valid current traffic still acts.
	for _, n := range []int{0, 1, 256} {
		p, l := metamorphicPeer(t, row, UDP)
		plain := append(unhex(unknownControlLiteral), bytes.Repeat([]byte{0xa5}, n)...)
		put16(plain[4:], uint16(n))
		before := *p.h.m
		out := metamorphicIngress(t, p, l, p.record(0xffff, plain))
		after := *p.h.m
		before.serial = after.serial
		before.discards = after.discards
		if !metamorphicOnlyTimer(out) || !reflect.DeepEqual(before, after) {
			t.Fatal("unknown inner opcode changed authority")
		}
		a := metamorphicIngress(t, p, l, metamorphicRecord(p, row, unhex(row.Plain)))
		found := false
		for _, x := range a {
			found = found || x.Kind == Seal && len(x.Data) >= 16 && le16(x.Data[8:]) == 0x0241
		}
		if !found {
			t.Fatal("unknown prevented current relay progress")
		}
	}
}
