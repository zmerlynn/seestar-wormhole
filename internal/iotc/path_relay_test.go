package iotc

import (
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"net/netip"
	"testing"
	"time"
)

func TestFreshPendingRelayAlternative(t *testing.T) {
	m, _ := New(config())
	m.started = true
	m.state = Connecting
	m.material = material()
	m.selection = true
	m.selected = UDP
	links := []Link{{Address: netip.MustParseAddrPort("203.0.113.1:10001")}, {Address: netip.MustParseAddrPort("203.0.113.2:10001")}}
	for _, l := range links {
		idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
		m.cache.Entries[idx].Key = 3
		m.cache.Entries[idx].CachedMarker = Marker{10, 1}
		m.add(endpoint{link: l, connected: true, stage: relayInfoPending, cache: idx, sid: 0x55})
	}
	m.relaySelected = links[0]
	h := newHarness(t, "silent", 0)
	for i, l := range links {
		b := append([]byte(nil), h.fixtures[0x030e]...)
		put32(b[36:], uint32(0x100+i))
		token := uint64(100 + i)
		m.pending[10] = operation{token: token, kind: Open, link: l, typ: 4, sid: 0x55, size: len(b)}
		out := m.Step(Event{Kind: Decrypted, Attempt: 1, Token: token, Data: b}, 0)
		for _, a := range out {
			if a.Kind == Seal {
				m.Step(Event{Kind: Encrypted, Attempt: 1, Token: a.Token, Data: make([]byte, len(a.Data)+16)}, 0)
			}
		}
	}
	m.Step(Event{Kind: LinkFailed, Attempt: 1, Link: links[1]}, 0)
	out := m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(10*time.Millisecond))
	for _, a := range out {
		if a.Kind == Ready && a.Link == links[0] && m.relay == 0x100 {
			return
		}
	}
	m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(59*time.Second))
	t.Fatalf("usable earlier relay stranded: state=%v pending=%v firstStage=%v allocation=%x", m.state, m.relayPending, m.endpoints[0].stage, m.relay)
}

// Both completion orders and either failed server must preserve the survivor's
// allocation, including after the edge has already sent both ACKs.
func TestReadyRelayPermutations(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for failed := 0; failed < 2; failed++ {
			t.Run(fmt.Sprintf("reverse=%t/failed=%d", reverse, failed), func(t *testing.T) {
				m := terminalMachine(t, 0)
				m.selected = UDP
				h := newHarness(t, "silent", 0)
				links := []Link{{Address: netip.MustParseAddrPort("203.0.113.1:10001")}, {Address: netip.MustParseAddrPort("203.0.113.2:10001")}}
				for _, l := range links {
					idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
					m.cache.Entries[idx].Key = 3
					m.cache.Entries[idx].CachedMarker = Marker{10, 1}
					m.add(endpoint{link: l, connected: true, stage: relayInfoPending, cache: idx, sid: 0x55})
				}
				order := []int{0, 1}
				if reverse {
					order = []int{1, 0}
				}
				for _, i := range order {
					b := append([]byte(nil), h.fixtures[0x030e]...)
					put32(b[36:], uint32(0x100+i))
					token := m.token()
					m.reserve(operation{token: token, kind: Open, link: links[i], typ: 4, sid: 0x55, size: len(b)})
					out := m.Step(Event{Kind: Decrypted, Attempt: 1, Token: token, Data: b}, 0)
					for _, a := range out {
						if a.Kind == Seal {
							m.Step(Event{Kind: Encrypted, Attempt: 1, Token: a.Token, Data: make([]byte, len(a.Data)+16)}, 0)
						}
					}
				}
				m.Step(Event{Kind: LinkFailed, Attempt: 1, Link: links[failed]}, 0)
				m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(10*time.Millisecond))
				survivor := 1 - failed
				if m.state != Established || m.Direct() || m.path != links[survivor] {
					t.Fatalf("failed %v: got state=%v path=%v, want %v", links[failed], m.state, m.path, links[survivor])
				}
				out := m.Step(Event{Kind: Write, Attempt: 1, Data: []byte{1}}, core.Instant(11*time.Millisecond))
				for _, a := range out {
					if a.Kind == Send {
						p, err := DecodeLegacy(a.Data, m.key)
						if err != nil || p.ID != uint16(0x100+survivor) {
							t.Fatalf("surviving relay data allocation=%x err=%v", p.ID, err)
						}
						return
					}
				}
				t.Fatal("selected relay did not send data")
			})
		}
	}
}
