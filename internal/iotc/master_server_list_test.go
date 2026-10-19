package iotc

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

// Independent headers and sockaddr/key-slot bytes; no production encoder
// constructs these units.
func TestMasterServerListReplacement(t *testing.T) {
	const head = "04021d001c0100000c10180000000000214300004558414d504c45554944303030303030303030300000000000000000000000000000000002009c40c000020a000000000000000000000200"
	const first = "09006c0002002711cb0071010000000000000000"
	const second = "09006c0002002712cb0071020000000000000000"
	raw := append(unhex(head+first), make([]byte, 92)...)
	raw = append(raw, unhex(second)...)
	raw = append(raw, make([]byte, 92)...)
	p, err := ParsePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseControl(p)
	if err != nil || len(c.Servers) != 1 || c.Servers[0].Address.String() != "203.0.113.2:10002" {
		t.Fatalf("later type 9 must replace earlier: %+v, %v", c.Servers, err)
	}
	// Declared unit count, independent of surplus bytes. A final server-list
	// count zero skips installation and query advance.
	p.Body[58] = 1
	c, err = ParseControl(p)
	if err != nil || len(c.Servers) != 1 || c.Servers[0].Address.String() != "203.0.113.1:10001" {
		t.Fatalf("trailing unit examined: %+v, %v", c.Servers, err)
	}
	p.Body[58] = 2
	for _, n := range []int{60, 61, 62, 63, 64, 171, 172, 173, 174, 175, 279, 283} {
		if _, err := ParseControl(Packet{Opcode: 0x100c, Body: p.Body[:min(n, len(p.Body)-1)]}); err == nil {
			t.Errorf("accepted extent truncated at %d", n)
		}
	}
}

// The final type-9 count alone gates install/advance; separate results merge,
// never prune.
func TestMasterFinalServerCountAndMerge(t *testing.T) {
	const prefix = "214300004558414d504c45554944303030303030303030300000000000000000000000000000000002009c40c000020a00000000000000000000"
	const addr1 = "02002711cb0071010000000000000000"
	const addr2 = "02002712cb0071020000000000000000"
	const addr3 = "02002713cb0071030000000000000000"
	// Keys need not be valid EC points to test metadata installation; no derive
	// completion is supplied. All 92 bytes are independently specified here.
	key1 := strings.Repeat("11", 92)
	key2 := strings.Repeat("22", 92)
	key3 := strings.Repeat("33", 92)
	parse := func(count string, units string) Control {
		t.Helper()
		c, err := ParseControl(Packet{Opcode: 0x100c, Body: unhex(prefix + count + units)})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	nonempty := "09006c00" + addr1 + key1
	for _, transport := range []Transport{UDP, TCP} {
		t.Run(fmt.Sprint(transport), func(t *testing.T) {
			m, err := New(config())
			if err != nil {
				t.Fatal(err)
			}
			m.state, m.material = Connecting, material()
			m.count = 1
			m.endpoints[0] = endpoint{master: true, connected: true, link: Link{Transport: transport, Address: netip.MustParseAddrPort("192.0.2.10:10001")}}
			for _, empty := range []Control{
				parse("0000", ""), parse("0100", "09000300aabbcc"),
				parse("0200", nonempty+"09000000"),
				parse("0200", nonempty+"09000300aabbcc"),
			} {
				m.masterResult(0, empty, Marker{10, 1})
				if len(empty.Servers) != 0 || m.cache.Count != 0 || m.endpoints[0].stage != endpointInitial || m.udpResponse || m.count != 1 || m.state != Connecting {
					t.Fatalf("zero final count installed/advanced: servers=%v cache=%+v endpoint=%+v", empty.Servers, m.cache, m.endpoints[0])
				}
			}
			m.masterResult(0, parse("0100", "0900d800"+addr1+key1+addr2+key2), Marker{10, 1})
			if m.cache.Count != 2 || m.endpoints[0].stage != masterAnswered || m.udpResponse != (transport == UDP) {
				t.Fatalf("nonempty did not install/advance: cache=%+v endpoint=%+v", m.cache, m.endpoints[0])
			}
			before := m.cache
			count := m.count
			m.masterResult(0, parse("0100", "09000000"), Marker{20, 2})
			if m.cache != before || m.count != count || m.endpoints[0].stage != masterAnswered {
				t.Fatal("empty later 100c erased or changed stored state")
			}
			m.masterResult(0, parse("0100", "0900d800"+addr2+key3+addr3+key1), Marker{20, 2})
			if m.cache.Count != 3 || m.cache.Entries[0] != before.Entries[0] || m.cache.Entries[1].Public[91] != 0x33 || m.cache.Entries[1].Marker != (Marker{20, 2}) || m.cache.Entries[2].Address.String() != "203.0.113.3" {
				t.Fatalf("list must update known, append new, keep absent: %+v", m.cache)
			}
		})
	}
}
