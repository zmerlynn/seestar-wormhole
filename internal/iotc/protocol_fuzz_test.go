package iotc

import (
	"encoding/base64"
	"github.com/go-json-experiment/json"
	"os"
	"testing"
)

func FuzzProtocolParsers(f *testing.F) {
	raw, _ := os.ReadFile("testdata/modern-fixtures.json")
	var fs []struct{ Hex string }
	_ = json.Unmarshal(raw, &fs, json.MatchCaseInsensitiveNames(true))
	for _, v := range fs {
		b := unhex(v.Hex)
		f.Add(le16(b[8:]), b[16:])
	}
	for _, op := range []uint16{0x0218, 0x0401, 0x0402, 0x0403, 0x0404, 0x0405, 0x0406} {
		n := 36
		if op == 0x0218 {
			n = 56
		}
		f.Add(op, make([]byte, n))
	}
	f.Add(uint16(0), unhex("02002711cb0071010000000000000000"))
	f.Add(uint16(0), []byte(`{"realm":"test"}`))
	f.Fuzz(func(t *testing.T, op uint16, b []byte) {
		if len(b) > 16384 {
			return
		}
		ParseControl(Packet{Opcode: op, Flags: 8, Body: b})
		ParseData(Packet{Opcode: op, Flags: 8, Body: b})
		ParseAlert(b)
		ParseAddress(b)
		ParseLicenseJSON(b)
		wire := append([]byte{1, 0, 0, 0}, make([]byte, 128)...)
		wire = append(wire, b...)
		_, payload, err := ParseLicenseEnvelope(base64.StdEncoding.EncodeToString(wire))
		if err == nil {
			_, _ = UnmaskLicense(payload, [128]byte{})
		}
		_, _ = DecodeLegacy(b, [16]byte{})
		_, _ = ParsePacket(b)
		_, _ = ParseRecord(b)
	})
}
func FuzzWireEvents(f *testing.F) {
	for _, op := range []uint16{0x8004, 0x8005} {
		p := controlPacket(op, 0x3f, 24)
		copy(p.Body, unhex("02002711cb0071010000000000000000"))
		put32(p.Body[16:], 77)
		put16(p.Body[20:], 7)
		b, _ := EncodeLegacy(p, ScrambleKey([16]byte{}))
		f.Add(byte(0), b)
	}
	f.Add(byte(1), unhex("51cc0000020310000700000000000000000000000000000000000000"))
	f.Fuzz(func(t *testing.T, mode byte, b []byte) {
		m := established(t, true)
		if mode%3 == 0 {
			m.state = Connecting
			m.add(endpoint{link: m.path, connected: true, hello: 77})
			m.cache.Count = 1
			m.cache.Entries[0].Marker = Marker{10, 1}
			m.cache.Entries[0].Address = m.path.Address.Addr()
		}
		if mode%3 == 1 {
			m.state = Connecting
			m.add(endpoint{link: m.path, master: true, connected: true})
		}
		m.Step(Event{Kind: Wire, Attempt: 1, Link: m.path, Data: b}, 0)
	})
}
