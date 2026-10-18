package iotc

import (
	"net/netip"
	"testing"
)

func TestIOTCDiscardCounters(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule int
		drop func(*Machine)
	}{
		{"stale_attempt", 39, func(m *Machine) { m.Step(Event{Kind: Wire, Attempt: 2}, 0) }},
		{"stale_link", 39, func(m *Machine) { m.linkFailed(Link{}) }},
		{"stale_material", 39, func(m *Machine) { m.materialReady(Event{}) }},
		{"stale_dns", 39, func(m *Machine) { m.resolved(Event{}) }},
		{"stale_crypto", 39, func(m *Machine) { m.cryptoDone(Event{}) }},
		{"stale_failure", 39, func(m *Machine) { m.effectFailed(Event{}) }},
		{"oversize", 40, func(m *Machine) { m.handleEvent(Event{Kind: Wire, Data: make([]byte, MaxPacket+1)}) }},
		{"crypto_capacity", 41, func(m *Machine) {
			for i := range m.pending {
				m.pending[i].token = 1
			}
			m.seal(Link{}, 1, 1, RawMasterRSA, 0, nil, nil)
		}},
		{"missing_control_key", 41, func(m *Machine) { m.control(0, 0x0240) }},
		{"legacy_encoding", 41, func(m *Machine) { m.legacy(Link{}, Packet{Body: make([]byte, MaxPacket+1)}) }},
		{"duplicate_seal", 41, func(m *Machine) {
			m.pending[0] = operation{token: 1, kind: Seal, opcode: 1}
			m.seal(Link{}, 1, 1, RawMasterRSA, 0, nil, nil)
		}},
		{"hello_history", 41, func(m *Machine) { m.endpoints[0].hello = 1; m.endpoints[0].helloCount = 119; m.hello(0) }},
		{"record_gate", 42, func(m *Machine) { m.openRecord(Event{Data: []byte{1}}) }},
		{"unknown_record", 43, func(m *Machine) { m.count = 1; m.openRecord(Event{Data: unhex("51cc00000263000000000000")}) }},
		{"decrypted_source", 44, func(m *Machine) { m.decrypted(operation{}, nil) }},
		{"decrypted_sid", 44, func(m *Machine) { m.count = 1; m.endpoints[0].sid = 1; m.decrypted(operation{typ: 4}, nil) }},
		{"precheck_stage", 45, func(m *Machine) { m.precheckResult(0, operation{}, Control{}) }},
		{"relay_stage", 45, func(m *Machine) { m.relayInfo(0, operation{}, Control{}) }},
		{"relay_id_narrowing", 45, func(m *Machine) {
			m.selection = true
			m.selected = UDP
			m.endpoints[0].stage = relayInfoPending
			m.cache.Count = 1
			m.cache.Entries[0].Key = 1
			m.relayInfo(0, operation{typ: 4}, Control{RelayID: 0x10001})
			if m.endpoints[0].relayID != 1 {
				t.Fatal("relay ID acceptance changed")
			}
		}},
		{"knock_direction", 48, func(m *Machine) {
			m.selection = true
			m.selected = UDP
			l := Link{Address: netip.MustParseAddrPort("192.0.2.1:1")}
			m.candidates[0] = l.Address
			m.candidateCount = 1
			m.peerKnock(l, knock(0x0403, m.cfg.UID, m.material.RID))
		}},
		{"empty_master", 46, func(m *Machine) { m.masterResult(0, Control{}, Marker{}) }},
		{"invalid_master_entry", 46, func(m *Machine) { m.masterResult(0, Control{Servers: []Server{{}}}, Marker{}) }},
		{"endpoint_capacity", 46, func(m *Machine) { m.count = 32; m.add(endpoint{}) }},
		{"candidate_invalid", 47, func(m *Machine) { m.addCandidate(netip.AddrPort{}, 0) }},
		{"candidate_capacity", 47, func(m *Machine) { m.candidateCount = 12; m.addCandidate(netip.MustParseAddrPort("192.0.2.1:1"), 0) }},
		{"peer_source", 48, func(m *Machine) { m.peer(Link{}, Packet{Opcode: 0x0408}) }},
		{"short_hello", 48, func(m *Machine) { m.observeHello(Link{}, Packet{}) }},
		{"nat_invalid", 49, func(m *Machine) { m.nat.Observe(netip.AddrPort{}, netip.AddrPort{}, false) }},
		{"nat_duplicate", 49, func(m *Machine) {
			a := netip.MustParseAddrPort("192.0.2.1:1")
			m.nat.Observe(a, a, false)
			m.nat.Observe(a, a, false)
		}},
		{"nat_changed", 49, func(m *Machine) {
			a := netip.MustParseAddrPort("192.0.2.1:1")
			m.nat.Observe(a, a, false)
			m.nat.Observe(a, netip.MustParseAddrPort("192.0.2.2:1"), false)
		}},
		{"nat_capacity", 49, func(m *Machine) {
			m.nat.count = 8
			a := netip.MustParseAddrPort("192.0.2.1:1")
			m.nat.Observe(a, a, false)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := New(Config{Attempt: 1, UID: [20]byte{1}, License: License{Realm: "x"}})
			if err != nil {
				t.Fatal(err)
			}
			m.started = true
			m.state = Connecting
			tc.drop(m)
			got := m.Discards()
			if got[tc.rule-39] != 1 {
				t.Fatalf("R%03d count = %d, want 1 (all %v)", tc.rule, got[tc.rule-39], got)
			}
			for i, n := range got {
				if i != tc.rule-39 && n != 0 {
					t.Fatalf("unexpected R%03d count %d", 39+i, n)
				}
			}
		})
	}
}
