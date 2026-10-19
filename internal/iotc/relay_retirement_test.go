package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"net/netip"
	"testing"
	"time"
)

func TestFailedPendingRelay(t *testing.T) {
	m, _ := New(config())
	m.started = true
	m.state = Connecting
	m.material = material()
	m.selection = true
	m.selected = TCP
	l := Link{TCP, netip.MustParseAddrPort("203.0.113.1:443"), 10}
	m.add(endpoint{link: l, connected: true, stage: relayInfoReady, sid: 0x55})
	m.relayPending = l
	m.Step(Event{Kind: LinkFailed, Attempt: 1, Link: l}, 0)
	out := m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(10*time.Millisecond))
	for _, a := range out {
		if a.Kind == Ready {
			t.Fatalf("emitted Ready for failed relay %v", a.Link)
		}
	}
}

func TestPendingRelayRevalidatesOwnership(t *testing.T) {
	for _, change := range []string{"generation", "disconnected", "stage"} {
		t.Run(change, func(t *testing.T) {
			m, _ := New(config())
			m.started = true
			m.state = Connecting
			m.material = material()
			m.selection = true
			m.selected = TCP
			l := Link{TCP, netip.MustParseAddrPort("203.0.113.1:443"), 10}
			m.add(endpoint{link: l, connected: true, stage: relayInfoReady})
			m.relayPending = l
			switch change {
			case "generation":
				m.endpoints[0].link.ID++
			case "disconnected":
				m.endpoints[0].connected = false
			case "stage":
				m.endpoints[0].stage = relayInfoPending
			}
			for _, a := range m.Step(Event{Kind: Tick, Attempt: 1}, 0) {
				if a.Kind == Ready {
					t.Fatalf("Ready from invalid %s", change)
				}
			}
		})
	}
}

func TestFailedRelayReleasesSelection(t *testing.T) {
	m, _ := New(config())
	m.started = true
	m.state = Connecting
	m.material = material()
	m.selection = true
	m.selected = TCP
	first := Link{TCP, netip.MustParseAddrPort("203.0.113.1:443"), 10}
	second := Link{TCP, netip.MustParseAddrPort("203.0.113.2:443"), 11}
	m.add(endpoint{link: first, connected: true, stage: relayInfoReady})
	m.relayPending = first
	m.relaySelected = first
	idx, _ := m.cache.Install(Server{Address: second.Address}, Marker{10, 1})
	m.cache.Entries[idx].Key = 3
	m.cache.Entries[idx].CachedMarker = Marker{10, 1}
	m.add(endpoint{link: second, connected: true, stage: rendezvousPending, cache: idx, sid: 0x55})
	m.Step(Event{Kind: LinkFailed, Attempt: 1, Link: first}, 0)
	q := controlPacket(0x020d, 0x24, 32)
	copy(q.Body, m.cfg.UID[:])
	copy(q.Body[24:], m.material.RID[:])
	b, _ := q.Marshal()
	m.pending[0] = operation{token: 99, kind: Open, link: second, typ: 4, sid: 0x55, size: len(b)}
	m.Step(Event{Kind: Decrypted, Attempt: 1, Token: 99, Data: b}, 0)
	if m.relaySelected != second || m.endpoints[1].stage != relayInfoPending {
		t.Fatal("failed selection blocked usable competing relay")
	}
}
