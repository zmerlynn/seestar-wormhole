package iotc

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"net/netip"
	"testing"
	"time"
)

func established(t *testing.T, direct bool) *Machine {
	t.Helper()
	m, e := New(config())
	if e != nil {
		t.Fatal(e)
	}
	m.started = true
	m.material = material()
	m.hasMaterial = true
	m.state = Connecting
	m.selection = true
	m.selected = UDP
	l := Link{Address: netip.MustParseAddrPort("198.51.100.20:41000")}
	m.candidates[0] = l.Address
	m.candidateCount = 1
	m.relay = 0x2345
	m.establish(l, direct)
	m.out = nil
	return m
}
func incoming(m *Machine, op uint16, payload []byte) Event {
	p := m.sessionPacket(op, payload)
	p.Route = 0x12
	p.Type = 1
	b, _ := EncodeLegacy(p, m.key)
	return Event{Kind: Wire, Attempt: 1, Link: m.path, Data: b}
}
func TestLivenessStrictBoundaryAndReset(t *testing.T) {
	m := established(t, true)
	m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(31*time.Second))
	if m.state != Established || m.alive != 31 {
		t.Fatal("expired before strict threshold")
	}
	m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(32*time.Second))
	if m.state != Closed {
		t.Fatal("did not expire on tick32")
	}
	m = established(t, true)
	m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(30*time.Second))
	e := incoming(m, 0x0428, m.material.RID[:])
	m.Step(e, core.Instant(30*time.Second))
	m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(31*time.Second))
	if m.alive != 1 {
		t.Fatalf("alive reset: %d", m.alive)
	}
	m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(2*time.Second))
	if m.alive != 1 {
		t.Fatal("regression advanced counter")
	}
}
func TestPeerIdentityAndDatagramOwnership(t *testing.T) {
	m := established(t, true)
	body := append([]byte{12, 0, 0, 0}, m.material.RID[:]...)
	body = append(body, 0x17, 0xfe, 0xfd)
	good := incoming(m, 0x0408, body)
	for _, change := range []string{"source", "rid", "direction", "channel", "datatype", "length"} {
		t.Run(change, func(t *testing.T) {
			e := good
			e.Data = append([]byte(nil), good.Data...)
			p, _ := DecodeLegacy(e.Data, m.key)
			switch change {
			case "source":
				e.Link.Address = netip.MustParseAddrPort("198.51.100.21:41000")
			case "rid":
				p.Body[4] ^= 1
			case "direction":
				p.Opcode = 0x0407
			case "channel":
				p.Channel = 1
			case "datatype":
				p.Type = 0
			case "length":
				p.Body[0] = 11
			}
			e.Data, _ = EncodeLegacy(p, m.key)
			for _, a := range m.Step(e, 0) {
				if a.Kind == Received {
					t.Fatal("accepted foreign payload")
				}
			}
		})
	}
	var data []byte
	for _, a := range m.Step(good, 0) {
		if a.Kind == Received {
			data = a.Data
		}
	}
	if !bytes.Equal(data, []byte{0x17, 0xfe, 0xfd}) {
		t.Fatal("lost packet boundary")
	}
	clear(good.Data)
	if !bytes.Equal(data, []byte{0x17, 0xfe, 0xfd}) {
		t.Fatal("borrowed event storage escaped")
	}
}
func TestCloseCountsAndIsolation(t *testing.T) {
	for _, direct := range []bool{true, false} {
		m := established(t, direct)
		out := m.Step(Event{Kind: Cancel, Attempt: 1}, 0)
		count := 0
		for _, a := range out {
			if a.Kind == Send {
				count++
			}
		}
		want := 5
		if direct {
			want = 3
		}
		if count != want {
			t.Fatalf("close packets %d want %d", count, want)
		}
		if m.material != (Material{}) || m.cache.Count != 0 {
			t.Fatal("retained secret context")
		}
		if len(m.Step(Event{Kind: Cancel, Attempt: 1}, 0)) != 0 {
			t.Fatal("double disposal")
		}
	}
}
func TestRelayReadyNeedsInfoAndDirectWinsPoll(t *testing.T) {
	m := established(t, true)
	m.state = Connecting
	m.relayPending = Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	q := knock(0x0405, m.cfg.UID, m.material.RID)
	q.Body[28] = 1
	b, _ := EncodeLegacy(q, m.key)
	m.Step(Event{Kind: Wire, Attempt: 1, Link: m.path, Data: b}, 0)
	m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(10*time.Millisecond))
	if !m.direct || m.state != Established {
		t.Fatal("relay overtook direct in same selector interval")
	}
}
func TestStaleTokenAndCiphertextBudget(t *testing.T) {
	m, _ := New(config())
	m.started = true
	m.state = Connecting
	m.material = material()
	m.hasMaterial = true
	l := Link{Address: netip.MustParseAddrPort("192.0.2.100:10240")}
	m.add(endpoint{link: l, master: true, connected: true})
	b, _ := RecordHeader(1, 7, 32)
	b = append(b, make([]byte, 32)...)
	e := Event{Kind: Wire, Attempt: 1, Link: l, Data: b}
	for j := 0; j < 10000; j++ {
		m.Step(e, 0)
	}
	n := 0
	var token uint64
	for _, p := range m.pending {
		if p.token != 0 {
			n++
			token = p.token
		}
	}
	if n != 1 {
		t.Fatalf("pending decrypts %d", n)
	}
	m.Step(Event{Kind: Decrypted, Attempt: 1, Token: token + 1, Data: make([]byte, 16)}, 0)
	if !m.pendingFor(l, Open, 0) {
		t.Fatal("wrong token consumed completion")
	}
	m.Step(Event{Kind: CryptoRejected, Attempt: 1, Token: token}, 0)
	if m.pendingFor(l, Open, 0) {
		t.Fatal("slot not released")
	}
}
func TestRecordAADAndMasterPlaintext(t *testing.T) {
	m, _ := New(config())
	m.Step(Event{Kind: Start, Attempt: 1}, 0)
	m.Step(Event{Kind: MaterialReady, Attempt: 1, Token: m.materialToken, Material: material()}, 0)
	a := m.Step(Event{Kind: Resolved, Attempt: 1, Token: m.resolveToken, Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.100")}}, 0)
	var seal Action
	for _, v := range a {
		if v.Kind == Seal {
			seal = v
			break
		}
	}
	if !bytes.Equal(seal.AAD, unhex("51cc00000201000107000000")) || len(seal.Data) != 256 {
		t.Fatal("master envelope")
	}
	b := seal.Data
	if le16(b[16:]) != 0x4321 || b[20] != 1 || b[36] != 2 || string(b[48:68]) != "EXAMPLEUID0000000000" || string(b[68:72]) != "test" || b[84] != 6 {
		t.Fatalf("synthetic master fields: %x", b[:88])
	}
	if !bytes.Equal(b[88:], make([]byte, 168)) {
		t.Fatal("RSA right padding")
	}
}
func TestDecoderAllSplitsAndCoalescing(t *testing.T) {
	p := controlPacket(0x8003, 0x3f, 8)
	key := ScrambleKey([16]byte{})
	legacy, _ := EncodeLegacy(p, key)
	modern := unhex("51cc0000020304000700000001020304")
	whole := append(append([]byte{}, legacy...), modern...)
	for split := 0; split <= len(whole); split++ {
		var d Decoder
		one, e := d.Feed(whole[:split], key)
		if e != nil {
			t.Fatal(e)
		}
		two, e := d.Feed(whole[split:], key)
		if e != nil {
			t.Fatal(e)
		}
		all := append(one, two...)
		if len(all) != 2 || !bytes.Equal(all[0], legacy) || !bytes.Equal(all[1], modern) || d.Buffered() != 0 {
			t.Fatalf("split %d", split)
		}
	}
}
func TestLicenseDiscovery(t *testing.T) {
	for _, v := range []struct{ json, region, want string }{{`{"realm":"foo"}`, "", "all-c-master-foo.iotcplatform.com"}, {`{"realm":"foo"}`, "cn", "cn-c-master-foo.kalay.net.cn"}, {`{"realm":"foo.example"}`, "eu", "eu-c-master-foo.example"}, {`{"realm":"foo","standalone":{"all":"custom.example"}}`, "", "custom.example"}} {
		l, e := ParseLicenseJSON([]byte(v.json))
		if e != nil {
			t.Fatal(e)
		}
		got, e := l.MasterDomain(v.region)
		if e != nil || got != v.want {
			t.Fatalf("got %q want %q", got, v.want)
		}
	}
	for _, b := range []string{`{}`, `{"realm":1}`, `{"realm":"a","keyMask":"AA=="}`, `{"realm":"bad host"}`} {
		if _, e := ParseLicenseJSON([]byte(b)); e == nil {
			t.Fatal("accepted malformed license")
		}
	}
}

func TestLateUDPPrecheckParticipates(t *testing.T) {
	m, _ := New(config())
	m.started = true
	m.state = Connecting
	m.material = material()
	m.selection = true
	m.selected = UDP
	l := Link{Address: netip.MustParseAddrPort("192.0.2.1:10001")}
	idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
	m.cache.Entries[idx].Key = 3
	m.cache.Entries[idx].CachedMarker = Marker{10, 1}
	m.add(endpoint{link: l, connected: true, cache: idx, stage: precheckPending})
	q := controlPacket(0x0218, 0x24, 56)
	put16(q.Body, 0x1234)
	put32(q.Body[4:], 0x55)
	copy(q.Body[8:], m.cfg.UID[:])
	b, _ := q.Marshal()
	m.pending[0] = operation{token: 8, kind: Open, link: l, typ: 3, size: len(b)}
	out := m.Step(Event{Kind: Decrypted, Attempt: 1, Token: 8, Data: b}, 0)
	sent := false
	for _, a := range out {
		if a.Kind == Seal && le16(a.Data[8:]) == 0x020c {
			sent = true
		}
	}
	if !sent {
		t.Fatal("late usable UDP server stranded after transport selection")
	}
}
func TestEstablishedCancelsRendezvousSeals(t *testing.T) {
	m := established(t, true)
	m.state = Connecting
	m.pending[0] = operation{token: 4, kind: Seal, typ: 4, opcode: 0x020c, link: Link{Address: netip.MustParseAddrPort("192.0.2.1:10001")}, size: 64, aadN: 16}
	m.establish(m.path, true)
	for _, a := range m.Step(Event{Kind: Encrypted, Attempt: 1, Token: 4, Data: make([]byte, 80)}, 0) {
		if a.Kind == Send {
			t.Fatal("late rendezvous send after success")
		}
	}
}

func TestHelloSixteenByteReplyRejected(t *testing.T) {
	m, _ := New(config())
	m.started = true
	m.state = Connecting
	m.material = material()
	l := Link{Address: netip.MustParseAddrPort("192.0.2.1:10001")}
	idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
	m.add(endpoint{link: l, connected: true, cache: idx, hello: 1})
	q := controlPacket(0x8004, 0x3f, 16)
	copy(q.Body, addressBytes(netip.MustParseAddrPort("198.51.100.1:40000")))
	b, _ := EncodeLegacy(q, m.key)
	out := m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: b}, 0)
	for _, a := range out {
		if a.Kind != ArmTimer {
			t.Fatalf("uncorrelated short hello emitted %v, want only timer", a.Kind)
		}
	}
	if m.nat.count != 0 {
		t.Fatal("short hello changed NAT observations")
	}
}
func TestAlertCorrelationBeforeSIDCommit(t *testing.T) {
	for _, wrongNonce := range []bool{false, true} {
		m, _ := New(config())
		m.started = true
		m.state = Connecting
		m.material = material()
		l := Link{Address: netip.MustParseAddrPort("192.0.2.1:10001")}
		m.add(endpoint{link: l, stage: precheckPending})
		// Another server is still usable; inspect this path's terminal result.
		m.add(endpoint{link: Link{Address: netip.MustParseAddrPort("192.0.2.2:10001")}})
		b := unhex("55000000341201021d")
		if wrongNonce {
			b[4] ^= 1
		}
		m.pending[0] = operation{token: 1, kind: Open, link: l, typ: 14, size: 9}
		m.Step(Event{Kind: Decrypted, Attempt: 1, Token: 1, Data: b}, 0)
		if wrongNonce {
			if m.endpoints[0].sid != 0 || m.endpoints[0].stage != precheckPending {
				t.Fatal("bad nonce committed alert")
			}
		} else if m.endpoints[0].sid != 0x55 || m.endpoints[0].stage != endpointTerminal {
			t.Fatal("opaque protocol value prevented correlated alert")
		}
	}
}

func TestNewPunchDoesNotInheritKnockReply(t *testing.T) {
	h := newHarness(t, "silent", 0)
	m := h.m
	m.started = true
	m.state = Connecting
	m.material = material()
	m.selection = true
	m.selected = UDP
	m.candidates[0] = netip.MustParseAddrPort("192.0.2.99:9999")
	m.candidateCount = 1
	m.knockReply[0] = true
	server := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	m.add(endpoint{link: server, stage: rendezvousPending, sid: 0x55})
	raw := h.fixtures[0x030d]
	m.pending[0] = operation{token: 4, kind: Open, link: server, typ: 4, sid: 0x55, size: len(raw)}
	m.Step(Event{Kind: Decrypted, Attempt: 1, Token: 4, Data: raw}, 0)
	q := knock(0x0404, m.cfg.UID, m.material.RID)
	b, _ := EncodeLegacy(q, m.key)
	m.Step(Event{Kind: Wire, Attempt: 1, Link: Link{Address: netip.MustParseAddrPort("198.51.100.20:41000")}, Data: b}, 0)
	if m.state != Connecting {
		t.Fatal("new candidate inherited old endpoint handshake")
	}
}

func TestModernIdentityGuards(t *testing.T) {
	for _, field := range []string{"uid", "rid", "nonce", "server-sid", "valid"} {
		t.Run(field, func(t *testing.T) {
			h := newHarness(t, "silent", 0)
			m := h.m
			m.started = true
			m.state = Connecting
			m.material = material()
			m.selection = true
			m.selected = UDP
			l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
			m.add(endpoint{link: l, stage: rendezvousPending, sid: 0x55})
			raw := append([]byte(nil), h.fixtures[0x030d]...)
			sid := uint32(0x55)
			switch field {
			case "uid":
				raw[20] ^= 1
			case "rid":
				raw[120] ^= 1
			case "nonce":
				raw[16] ^= 1
			case "server-sid":
				sid++
			}
			m.pending[0] = operation{token: 1, kind: Open, link: l, typ: 4, sid: sid, size: len(raw)}
			m.Step(Event{Kind: Decrypted, Attempt: 1, Token: 1, Data: raw}, 0)
			if (m.candidateCount > 0) != (field == "valid") {
				t.Fatal("modern identity correlation")
			}
			if field == "valid" {
				for _, a := range m.Step(Event{Kind: Decrypted, Attempt: 1, Token: 1, Data: raw}, 0) {
					if a.Kind == Send {
						t.Fatal("replayed completion produced wire effect")
					}
				}
			}
		})
	}
}
func TestModernOuterSourceAndSID(t *testing.T) {
	for _, field := range []string{"source", "sid", "valid"} {
		t.Run(field, func(t *testing.T) {
			m, _ := New(config())
			m.started = true
			m.state = Connecting
			m.material = material()
			l := Link{Address: netip.MustParseAddrPort("192.0.2.100:10240")}
			m.add(endpoint{link: l, master: true, connected: true})
			sid := uint32(7)
			if field == "source" {
				l.Address = netip.MustParseAddrPort("192.0.2.101:10240")
			}
			if field == "sid" {
				sid = 8
			}
			b, _ := RecordHeader(1, sid, 32)
			b = append(b, make([]byte, 32)...)
			out := m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: b}, 0)
			opened := false
			for _, a := range out {
				if a.Kind == Open {
					opened = true
				}
			}
			if opened != (field == "valid") {
				t.Fatal("source/context guard")
			}
		})
	}
}

func TestLateInputNeverArmsPastDeadline(t *testing.T) {
	m := established(t, true)
	a := m.Step(Event{Kind: Write, Attempt: 1, Data: []byte{1}}, core.Instant(2*time.Second))
	for _, v := range a {
		if v.Kind == ArmTimer && v.At < core.Instant(2*time.Second) {
			t.Fatal("late input armed past deadline")
		}
	}
}
func TestExhaustedClockCannotSpin(t *testing.T) {
	m, _ := New(config())
	m.Step(Event{Kind: Start, Attempt: 1}, core.Instant(1<<63-1))
	if m.state != Closed {
		t.Fatal("exhausted monotonic range retained immediate timer")
	}
}
