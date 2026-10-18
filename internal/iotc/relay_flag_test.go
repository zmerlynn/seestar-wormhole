package iotc

import (
	"bytes"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

// Literal fixtures cover complete modern control layouts and every written
// field. Identities are synthetic; local NAT is 2.
// Flag 0 is the eligible unselected UDP / already-connected response variant.
const selectedRelayInfoHex = "04021d002c00000041022400000000004558414d504c4555494430303030303030303030443322118877665501000000000803041d00000002000000"
const unselectedRelayInfoHex = "04021d002c00000041022400000000004558414d504c4555494430303030303030303030443322118877665500000000000803041d00000002000000"
const relayInfoAADHex = "51cc0000020450000700000055000000"

func TestRelayInfoLiteral(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selected bool
		hex      string
	}{
		{"selected UDP or TCP", true, selectedRelayInfoHex},
		{"eligible unselected UDP or connected response", false, unselectedRelayInfoHex},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := rendezvous(0x0241, config().UID, material(), 2, 0x2345, tc.selected)
			got, err := p.Marshal()
			if err != nil || !bytes.Equal(got, unhex(tc.hex)) {
				t.Fatalf("0241 = %x, want %s; err=%v", got, tc.hex, err)
			}
		})
	}
}

func TestRelayInfoSelectionAndRetry(t *testing.T) {
	m, _ := New(config())
	m.started, m.state, m.material, m.selection, m.selected = true, Connecting, material(), true, UDP
	for j := range 2 {
		l := Link{Address: netip.MustParseAddrPort(fmt.Sprintf("192.0.2.%d:10001", j+1))}
		idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
		m.cache.Entries[idx].Key, m.cache.Entries[idx].CachedMarker = 3, Marker{10, 1}
		// Peer NAT deliberately differs from local NAT; H+56 must remain local.
		m.add(endpoint{link: l, connected: true, cache: idx, stage: rendezvousPending, sid: 0x55, peerNAT: 10})
	}
	for j := range 2 {
		m.out = nil
		m.relayResult(j, operation{typ: 4}, Control{RID: material().RID})
		want := selectedRelayInfoHex
		if j == 1 {
			want = unselectedRelayInfoHex
		}
		for retry := range 2 {
			if retry == 1 {
				m.out = nil
				// Model the edge completing the previous seal before the retry.
				clear(m.pending[:])
				m.now = m.now.Add(time.Second)
				m.retryEndpoint(j)
			}
			infos := 0
			for _, a := range m.out {
				if a.Kind == Seal && le16(a.Data[8:]) == 0x0241 {
					infos++
					if !bytes.Equal(a.Data, unhex(want)) || !bytes.Equal(a.AAD, unhex(relayInfoAADHex)) {
						t.Fatalf("server=%d retry=%d: plaintext=%x AAD=%x, want %s / %s", j, retry, a.Data, a.AAD, want, relayInfoAADHex)
					}
				}
			}
			if infos != 1 {
				t.Fatalf("server=%d retry=%d: info requests=%d, want 1", j, retry, infos)
			}
		}
	}
}

func TestSelectedUDPRelayProgress(t *testing.T) {
	h := newHarness(t, "relay", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	infos := 0
	runFiltered(h, 3*time.Second, func(p sim.Packet, _ core.Instant) bool {
		if p.To == "client" || len(p.Data) < 12 || le32(p.Data) != 0xcc51 {
			return false
		}
		plain, err := h.requestPlain(h.links[p.To], p.Data)
		if len(plain) < 16 || le16(plain[8:]) != 0x0241 {
			return false
		}
		if err != nil || !bytes.Equal(plain, unhex(selectedRelayInfoHex)) {
			t.Fatalf("selected UDP request=%x, want %s; err=%v", plain, selectedRelayInfoHex, err)
		}
		infos++
		return infos == 1 // Lose the first conforming request; retry must retain flag 1.
	})
	p := h.m.RelayProgress()
	if infos < 2 || h.ready != 1 || !h.relayACK[h.m.path] || p.Phase != RelaySelected || !p.Selected || p.InfoReceived == 0 || p.InfoOutstanding != 0 || h.readyProgress != p {
		t.Fatalf("infos=%d Ready=%d ACK=%v progress=%+v, want retried, acknowledged, selected relay", infos, h.ready, h.relayACK[h.m.path], p)
	}
	h.step(Event{Kind: Write, Data: []byte{0x71}})
	h.run(20 * time.Millisecond)
	if h.received != 1 {
		t.Fatalf("application receipts=%d, want 1", h.received)
	}
}

// This is a sender-conformance oracle, not a claim about the real backend's
// sufficient conditions for 030e. Corrupt each documented field independently.
func TestSimulatorRelayInfoGate(t *testing.T) {
	h := newHarness(t, "direct", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	h.run(time.Second)
	if h.ready != 1 {
		t.Fatal("fixture key exchange did not establish")
	}
	l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	for _, off := range []int{-1, 0, 2, 3, 4, 6, 8, 10, 12, 14, 15, 16, 36, 44, 48, 52, 56} {
		t.Run(fmt.Sprintf("field_%d", off), func(t *testing.T) {
			plain := unhex(selectedRelayInfoHex)
			if off >= 0 {
				plain[off] ^= 1
			}
			// Opcode corruption goes through the peer's validation boundary;
			// other 0241 corruptions exercise the actual encrypted server handler.
			if off == 8 {
				if h.validateRequest(plain) == nil {
					t.Fatal("peer accepted wrong opcode")
				}
				return
			}
			aad := unhex(relayInfoAADHex)
			pair := h.peerPairs[l.Address.Addr()]
			h.server(l, append(aad, pair.aead.Seal(nil, pair.iv[:], plain, aad)...))
			replies := 0
			runFiltered(h, 10*time.Millisecond, func(p sim.Packet, _ core.Instant) bool {
				if p.To == "client" {
					if len(p.Data) < 32 {
						t.Fatalf("short relay-info reply: %x", p.Data)
					}
					reply, err := pair.aead.Open(nil, pair.iv[:], p.Data[16:], p.Data[:16])
					if err != nil || !bytes.Equal(reply, h.fixtures[0x030e]) {
						t.Fatalf("reply=%x, want literal 030e; err=%v", reply, err)
					}
					replies++
					return true
				}
				return false
			})
			want := 0
			if off == -1 {
				want = 1
			}
			if replies != want {
				t.Fatalf("030e replies=%d, want %d", replies, want)
			}
		})
	}
}
