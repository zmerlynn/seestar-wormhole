package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"net/netip"
	"testing"
	"time"
)

func config() Config {
	return Config{Attempt: 1, RecordSID: 7, UID: [20]byte([]byte("EXAMPLEUID0000000000")), License: License{Realm: "test"}, LocalNAT: 2}
}
func material() Material {
	return Material{HelloSerial: 0x76543210, RID: [8]byte{0x44, 0x33, 0x22, 0x11, 0x88, 0x77, 0x66, 0x55}, MasterNonce: 0x4321, P2PNonce: 0x1234, MasterKey: [16]byte{1}, MasterIV: [12]byte{2}, MasterRef: 1, PrivateRef: 2, Public: [92]byte{3}}
}
func TestAttemptOneShot(t *testing.T) {
	m, e := New(config())
	if e != nil {
		t.Fatal(e)
	}
	a := m.Step(Event{Kind: Start, Attempt: 1}, 0)
	if len(a) < 2 {
		t.Fatal("no effects")
	}
	m.Step(Event{Kind: Cancel, Attempt: 1}, 0)
	if a := m.Step(Event{Kind: Start, Attempt: 1}, 0); len(a) != 0 {
		t.Fatal("reused attempt")
	}
	if a := m.Step(Event{Kind: MaterialReady, Attempt: 1, Material: material()}, 0); len(a) != 0 {
		t.Fatal("resurrected")
	}
}
func TestCacheVendorBoundary(t *testing.T) {
	var c Cache
	s := Server{Address: netip.MustParseAddrPort("192.0.2.1:10001")}
	i, e := c.Install(s, Marker{10, 1})
	if e != nil {
		t.Fatal(e)
	}
	c.Entries[i].Key = 9
	c.Entries[i].CachedMarker = c.Entries[i].Marker
	s.Public[91] = 1
	c.Install(s, Marker{10, 2})
	if _, ok := c.Lookup(i); !ok {
		t.Fatal("same second refreshed vendor key")
	}
	s.Address = netip.MustParseAddrPort("192.0.2.1:443")
	j, _ := c.Install(s, Marker{11, 1})
	if i != j || c.Entries[i].Marker.Seconds != 10 {
		t.Fatal("unchanged slot/port changed marker")
	}
	s.Public[0] = 1
	c.Install(s, Marker{11, 2})
	if _, ok := c.Lookup(i); ok {
		t.Fatal("changed seconds reused key")
	}
	c.Clear()
	if c.Count != 0 {
		t.Fatal("retained cache")
	}
}
func TestTimeoutAndStale(t *testing.T) {
	m, _ := New(config())
	m.Step(Event{Kind: Start, Attempt: 1}, 0)
	if a := m.Step(Event{Kind: MaterialReady, Attempt: 2, Material: material()}, 0); len(a) != 0 {
		t.Fatal("stale generation")
	}
	a := m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(60*time.Second))
	found := false
	for _, v := range a {
		if v.Kind == Failed {
			found = true
		}
	}
	if !found {
		t.Fatal("no timeout")
	}
	if m.State() != Closed {
		t.Fatal("not closed")
	}
}

func TestRejectedCryptoReleasesSlot(t *testing.T) {
	m, _ := New(config())
	m.started = true
	m.state = Connecting
	m.material = material()
	l := Link{Address: netip.MustParseAddrPort("192.0.2.1:10240")}
	m.add(endpoint{link: l, master: true, connected: true})
	m.pending[0] = operation{token: 9, kind: Open, link: l}
	m.Step(Event{Kind: CryptoRejected, Attempt: 1, Token: 9}, 0)
	if m.pending[0].token != 0 || m.state != Connecting {
		t.Fatal("authentication failure retained slot or killed attempt")
	}
}
func TestDerivationMarkerCompletionRace(t *testing.T) {
	m, _ := New(config())
	m.started = true
	m.state = Connecting
	m.material = material()
	s := Server{Address: netip.MustParseAddrPort("192.0.2.1:10001")}
	i, _ := m.cache.Install(s, Marker{10, 1})
	m.add(endpoint{link: Link{Address: s.Address}, cache: i, stage: helloObserved, connected: true})
	m.prepare(0)
	token := m.cache.Entries[i].Pending
	s.Public[91] = 1
	m.cache.Install(s, Marker{11, 1})
	m.Step(Event{Kind: Derived, Attempt: 1, Token: token, Key: 3, IV: [12]byte{1}}, 0)
	if _, ok := m.cache.Lookup(i); ok {
		t.Fatal("old derivation labeled with new metadata")
	}
}
func TestTCPSelectsOneRelayInfoServer(t *testing.T) {
	m, _ := New(config())
	m.started = true
	m.state = Connecting
	m.material = material()
	m.selection = true
	m.selected = TCP
	for j := 0; j < 2; j++ {
		l := Link{TCP, netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 0, 2, byte(j + 1)}), 443), uint64(j + 1)}
		idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
		m.cache.Entries[idx].Key = 3
		m.cache.Entries[idx].CachedMarker = Marker{10, 1}
		m.add(endpoint{link: l, connected: true, cache: idx, stage: rendezvousPending, sid: 0x55})
	}
	for j := 0; j < 2; j++ {
		p := controlPacket(0x020d, 0x24, 32)
		copy(p.Body, m.cfg.UID[:])
		copy(p.Body[24:], m.material.RID[:])
		b, _ := p.Marshal()
		m.pending[j] = operation{token: uint64(j + 10), kind: Open, link: m.endpoints[j].link, typ: 4, sid: 0x55, size: len(b)}
		m.Step(Event{Kind: Decrypted, Attempt: 1, Token: uint64(j + 10), Data: b}, 0)
	}
	count := 0
	for _, p := range m.pending {
		if p.kind == Seal && p.opcode == 0x0241 && p.token != 0 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("relay info requests %d, want 1", count)
	}
}

// Normal selected UDP sends LE32 1 at H+44.
func TestSelectedUDPRelayInfoFlag(t *testing.T) {
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
	m.add(endpoint{link: l, connected: true, cache: idx, stage: rendezvousPending, sid: 0x55})
	q := controlPacket(0x020d, 0x24, 32)
	copy(q.Body, m.cfg.UID[:])
	copy(q.Body[24:], m.material.RID[:])
	b, _ := q.Marshal()
	m.pending[0] = operation{token: 99, kind: Open, link: l, typ: 4, sid: 0x55, size: len(b)}
	a := m.Step(Event{Kind: Decrypted, Attempt: 1, Token: 99, Data: b}, 0)
	for _, v := range a {
		if v.Kind == Seal && le16(v.Data[8:]) == 0x0241 {
			if got := le32(v.Data[44:]); got != 1 {
				t.Fatalf("selected UDP relay flag = %d, want 1", got)
			}
			return
		}
	}
	t.Fatal("no info request")
}
