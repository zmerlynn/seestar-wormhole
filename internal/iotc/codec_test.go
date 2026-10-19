package iotc

import (
	"bytes"
	"encoding/hex"
	"github.com/go-json-experiment/json"
	"os"
	"testing"
)

func unhex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
func TestScrambleEvidence(t *testing.T) {
	b, _ := os.ReadFile("testdata/synthetic-scramble-vectors.json")
	var v struct {
		Vectors []struct {
			Key, Input, Output string
			Partial            bool
		}
	}
	if err := json.Unmarshal(b, &v, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	for _, x := range v.Vectors {
		var key [16]byte
		copy(key[:], unhex(x.Key))
		got := Transform(unhex(x.Input), key, x.Partial, false)
		if !bytes.Equal(got, unhex(x.Output)) {
			t.Fatalf("length %d partial %v got %x want %s", len(got), x.Partial, got, x.Output)
		}
		if got := Transform(unhex(x.Output), key, x.Partial, true); !bytes.Equal(got, unhex(x.Input)) {
			t.Fatal("inverse differs from evidence")
		}
	}
}
func TestLiteralHeaders(t *testing.T) {
	b := unhex("04021d0b6c00010007042100443300010c0000004433221188776655")
	b = append(b, make([]byte, 96)...)
	p, err := ParsePacket(b)
	if err != nil {
		t.Fatal(err)
	}
	if p.Opcode != 0x0407 || p.ID != 0x3344 || p.Sequence != 1 || p.Route != 0x21 || p.Type != 1 {
		t.Fatalf("header: %+v", p)
	}
	rid, data, err := ParseData(p)
	if err != nil || rid != [8]byte{0x44, 0x33, 0x22, 0x11, 0x88, 0x77, 0x66, 0x55} || len(data) != 96 {
		t.Fatal("extension offsets")
	}
	got, err := p.Marshal()
	if err != nil || !bytes.Equal(got, b) {
		t.Fatal("literal encoding")
	}
	for _, bad := range [][]byte{b[:15], b[:len(b)-1], append(append([]byte{}, b...), 0)} {
		if _, err := ParsePacket(bad); err == nil {
			t.Fatal("accepted length")
		}
	}
}
func TestModernEvidence(t *testing.T) {
	b, _ := os.ReadFile("testdata/modern-fixtures.json")
	var fixtures []struct{ Opcode, Hex string }
	if err := json.Unmarshal(b, &fixtures, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Opcode, func(t *testing.T) {
			raw := unhex(f.Hex)
			p, err := ParsePacket(raw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := p.Marshal()
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatal("literal packet")
			}
			c, err := ParseControl(p)
			if err != nil {
				t.Fatal(err)
			}
			if c.UID != [20]byte([]byte("EXAMPLEUID0000000000")) {
				t.Fatalf("UID %q", c.UID)
			}
			switch p.Opcode {
			case 0x020d:
				if c.Result != -7 || c.RID[0] != 0x44 {
					t.Fatal("result/RID")
				}
			case 0x030e:
				if c.RelayID != 0x2345 || c.Peer.String() != "198.51.100.20:41000" || c.PeerNAT != 3 {
					t.Fatal("relay map")
				}
			case 0x030d:
				if c.Nonce != 0x1234 || c.RID[0] != 0x44 || c.WANCount != 2 {
					t.Fatal("punch map")
				}
			case 0x100c:
				if c.Nonce != 0x4321 || len(c.Servers) != 1 || c.Servers[0].Address.String() != "203.0.113.1:10001" {
					t.Fatal("master map")
				}
			}
		})
	}
}
func FuzzPacket(f *testing.F) {
	b, e := os.ReadFile("testdata/modern-fixtures.json")
	if e != nil {
		f.Fatal(e)
	}
	var fs []struct{ Hex string }
	if e = json.Unmarshal(b, &fs, json.MatchCaseInsensitiveNames(true)); e != nil {
		f.Fatal(e)
	}
	for _, v := range fs {
		f.Add(unhex(v.Hex))
	}
	f.Add(unhex("550000003412010202"))
	f.Add(unhex("51cc0000020310000700000000000000000000000000000000000000"))
	f.Add(unhex("04021d0a0800000028041200443300004433221188776655"))
	f.Fuzz(func(t *testing.T, b []byte) {
		if p, e := ParsePacket(b); e == nil {
			p.Marshal()
			ParseData(p)
			ParseControl(p)
		}
		DecodeLegacy(b, [16]byte{})
		ParseRecord(b)
		ParseAlert(b)
	})
}
func FuzzStream(f *testing.F) {
	p := controlPacket(0x8003, 0x3f, 8)
	b, _ := EncodeLegacy(p, [16]byte{})
	f.Add(b, uint8(1))
	f.Add(unhex("51cc0000020404000100000000000000"), uint8(3))
	f.Fuzz(func(t *testing.T, b []byte, n uint8) {
		if len(b) > 8192 {
			return
		}
		var d Decoder
		size := int(n) + 1
		for len(b) > 0 {
			k := min(size, len(b))
			if _, err := d.Feed(b[:k], [16]byte{}); err != nil {
				return
			}
			b = b[k:]
			if d.Buffered() > MaxPacket {
				t.Fatal("unbounded")
			}
		}
	})
}
func FuzzLicense(f *testing.F) {
	f.Add([]byte(`{"realm":"test","keyMask":null,"standalone":null,"nebulaDomainFromLicense":"true"}`))
	f.Add([]byte(`{"realm":"test","standalone":{"all":null}}`))
	f.Add([]byte(`{"realm":"test","keyMask":"AAAAAAAAAAAAAAAAAAAAAA=="}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		l, e := ParseLicenseJSON(b)
		if e == nil {
			l.MasterDomain("all")
		}
		ParseLicenseEnvelope(string(b))
		UnmaskLicense(b, [128]byte{})
		ParseAddress(b)
	})
}
