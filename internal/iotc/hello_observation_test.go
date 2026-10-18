package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"net/netip"
	"testing"
	"time"
)

func TestAlternateHelloAfterNormal(t *testing.T) {
	m, _ := New(config())
	m.started = true
	m.state = Connecting
	m.material = material()
	l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
	m.add(endpoint{link: l, connected: true, cache: idx, hello: 77})
	q := controlPacket(0x8004, 0x3f, 24)
	copy(q.Body, addressBytes(netip.MustParseAddrPort("198.51.100.1:40000")))
	put32(q.Body[16:], 77)
	put16(q.Body[20:], 7)
	b, _ := EncodeLegacy(q, m.key)
	m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: b}, 0)
	q.Opcode = 0x8005
	b, _ = EncodeLegacy(q, m.key)
	m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: b}, 1)
	if m.nat.Type() != 1 {
		t.Fatalf("correlated alternate hello after normal reply: got NAT %d, want 1", m.nat.Type())
	}
}

func TestAlternateHelloCorrelation(t *testing.T) {
	for _, variant := range []string{"other-port", "advertised-ip", "unknown-ip", "wrong-serial", "wrong-instance", "short-other", "normal-other"} {
		t.Run(variant, func(t *testing.T) {
			m, _ := New(config())
			m.started = true
			m.state = Connecting
			m.material = material()
			l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
			idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
			m.add(endpoint{link: l, connected: true, cache: idx, hello: 77, stage: precheckPending})
			m.cache.Install(Server{Address: netip.MustParseAddrPort("203.0.113.2:10001")}, Marker{10, 1})
			source := Link{Address: netip.MustParseAddrPort("203.0.113.1:10002")}
			q := controlPacket(0x8005, 0x3f, 24)
			copy(q.Body, addressBytes(netip.MustParseAddrPort("198.51.100.1:40000")))
			put32(q.Body[16:], 77)
			put16(q.Body[20:], 7)
			switch variant {
			case "advertised-ip":
				source.Address = netip.MustParseAddrPort("203.0.113.2:10002")
			case "unknown-ip":
				source.Address = netip.MustParseAddrPort("203.0.113.99:10002")
			case "wrong-serial":
				put32(q.Body[16:], 78)
			case "wrong-instance":
				put16(q.Body[20:], 8)
			case "short-other":
				q.Body = q.Body[:16]
			case "normal-other":
				q.Opcode = 0x8004
			}
			b, _ := EncodeLegacy(q, m.key)
			out := m.Step(Event{Kind: Wire, Attempt: 1, Link: source, Data: b}, 0)
			want := byte(0)
			if variant == "other-port" || variant == "advertised-ip" {
				want = 1
			}
			if m.nat.Type() != want {
				t.Fatalf("NAT=%d want=%d", m.nat.Type(), want)
			}
			if m.endpoints[0].stage != precheckPending {
				t.Fatal("NAT observation regressed endpoint")
			}
			for _, a := range out {
				if a.Kind == Seal || a.Kind == Derive {
					t.Fatal("NAT observation restarted precheck")
				}
			}
		})
	}
}

// Retries retain old authority, but cannot broaden its source/instance/window.
func TestDelayedHelloAdmission(t *testing.T) {
	for _, variant := range []string{"normal", "alternate", "unissued", "instance", "unknown-ip", "normal-other-port", "retired", "deadline", "stale-attempt", "ambiguous"} {
		t.Run(variant, func(t *testing.T) {
			m, _ := New(config())
			m.started, m.state, m.material, m.hasMaterial = true, Connecting, material(), true
			l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
			idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
			m.cache.Entries[idx].Key, m.cache.Entries[idx].CachedMarker = 3, Marker{10, 1}
			m.add(endpoint{link: l, connected: true, cache: idx})
			m.hello(0)
			first, err := DecodeLegacy(m.out[0].Data, m.key)
			if err != nil {
				t.Fatal(err)
			}
			m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(time.Second))
			q := controlPacket(0x8004, 0x3f, 24)
			copy(q.Body, addressBytes(netip.MustParseAddrPort("198.51.100.1:40000")))
			copy(q.Body[16:], first.Body[:6])
			at := core.Instant(1100 * time.Millisecond)
			attempt := uint64(1)
			switch variant {
			case "alternate":
				q.Opcode = 0x8005
				l.Address = netip.MustParseAddrPort("203.0.113.1:10002")
			case "unissued":
				put32(q.Body[16:], 0x76543210)
			case "instance":
				put16(q.Body[20:], 8)
			case "unknown-ip":
				q.Opcode = 0x8005
				l.Address = netip.MustParseAddrPort("203.0.113.99:10002")
			case "normal-other-port":
				l.Address = netip.MustParseAddrPort("203.0.113.1:10002")
			case "retired":
				m.Step(Event{Kind: LinkFailed, Attempt: 1, Link: l}, core.Instant(1050*time.Millisecond))
			case "deadline":
				at = core.Instant(60 * time.Second)
			case "stale-attempt":
				attempt = 2
			case "ambiguous":
				q.Opcode = 0x8005
				// Simulate a serial collision on another live endpoint; no arbitrary owner.
				other := Link{Address: netip.MustParseAddrPort("203.0.113.2:10001")}
				m.add(endpoint{link: other, connected: true, cache: idx, hello: le32(first.Body)})
			}
			b, _ := EncodeLegacy(q, m.key)
			out := m.Step(Event{Kind: Wire, Attempt: attempt, Link: l, Data: b}, at)
			precheck := false
			for _, a := range out {
				if a.Kind == Seal && le16(a.Data[8:]) == 0x0217 {
					precheck = true
				}
			}
			want := variant == "normal" || variant == "alternate"
			if precheck != want {
				t.Fatalf("precheck=%v want %v", precheck, want)
			}
			if !want && m.nat.Type() != 0 {
				t.Fatalf("inadmissible old reply changed NAT to %d", m.nat.Type())
			}
		})
	}
}
