package iotc

import (
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"net/netip"
	"testing"
	"time"
)

func TestDirectTaskLifetime(t *testing.T) {
	for _, ports := range []byte{0, 4} {
		for _, sumSix := range []bool{false, true} {
			t.Run(fmt.Sprintf("ports=%d/sumSix=%v", ports, sumSix), func(t *testing.T) {
				cfg := config()
				cfg.P2PPortCount = ports
				peer := byte(0)
				if sumSix {
					cfg.LocalNAT = 3
					peer = 3
				}
				m, _ := New(cfg)
				m.started = true
				m.state = Connecting
				m.material = material()
				link := Link{Address: netip.MustParseAddrPort("192.0.2.1:10001")}
				idx, _ := m.cache.Install(Server{Address: link.Address}, Marker{10, 1})
				m.cache.Entries[idx].Key = 3
				m.cache.Entries[idx].CachedMarker = Marker{10, 1}
				m.add(endpoint{link: link, connected: true, cache: idx, stage: precheckReady, peerNAT: peer, sid: 0x55})
				// Start late enough that a lifetime measured from attempt start fails.
				m.now = core.Instant(2 * time.Second)
				m.startRendezvous(0)
				m.pending = [64]operation{} // simulate completed initial sends
				requests := func(at time.Duration, opcode uint16) int {
					n := 0
					for _, a := range m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(2*time.Second+at)) {
						if a.Kind == Seal && le16(a.Data[8:]) == opcode {
							n++
						}
					}
					m.pending = [64]operation{} // observe each subsequent task emission
					return n
				}
				if n := requests(200*time.Millisecond, 0x0240); n != 1 {
					t.Fatalf("first retry=%d", n)
				}
				want := 1
				if sumSix {
					want = 0
				}
				if n := requests(400*time.Millisecond, 0x0240); n != want {
					t.Fatalf("400ms direct requests=%d, want %d", n, want)
				}
				if n := requests(time.Second, 0x020c); n != 1 {
					t.Fatalf("relay alternative lost: requests=%d", n)
				}
				if n := requests(2*time.Second, 0x0240); n != want {
					t.Fatalf("2s direct requests=%d, want %d", n, want)
				}
				if m.State() != Connecting {
					t.Fatal("direct expiry closed live relay")
				}
				// Ordinary parallel tasks remain eligible until the total 60s budget.
				if n := requests(57*time.Second, 0x0240); n != want {
					t.Fatalf("late direct requests=%d, want %d", n, want)
				}
				if n := requests(58*time.Second, 0x0240); n != 0 {
					t.Fatal("overall deadline emitted direct work")
				}
			})
		}
	}
}

func TestExpiredDirectEncryptionCannotSend(t *testing.T) {
	cfg := config()
	cfg.LocalNAT = 3
	m, _ := New(cfg)
	m.started = true
	m.state = Connecting
	m.material = material()
	link := Link{Address: netip.MustParseAddrPort("192.0.2.1:10001")}
	idx, _ := m.cache.Install(Server{Address: link.Address}, Marker{10, 1})
	m.cache.Entries[idx].Key = 3
	m.cache.Entries[idx].CachedMarker = Marker{10, 1}
	m.add(endpoint{link: link, connected: true, cache: idx, stage: precheckReady, peerNAT: 3, sid: 0x55})
	m.startRendezvous(0)
	var token uint64
	var ciphertext []byte
	for _, a := range m.out {
		if a.Kind == Seal && le16(a.Data[8:]) == 0x0240 {
			token = a.Token
			ciphertext = make([]byte, len(a.Data)+16)
		}
	}
	if token == 0 {
		t.Fatal("direct request not issued")
	}
	for _, a := range m.Step(Event{Kind: Encrypted, Attempt: 1, Token: token, Data: ciphertext}, core.Instant(400*time.Millisecond)) {
		if a.Kind == Send {
			t.Fatal("expired direct encryption was sent")
		}
	}
	if !m.endpoints[0].directExpired || m.endpoints[0].stage != rendezvousPending {
		t.Fatal("direct expiry lost relay ownership")
	}
}

// Expiry must revoke already-discovered direct peers as well as 0240 retries.
func TestDirectTaskExpiresDiscoveredCandidates(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint(shared), func(t *testing.T) {
			cfg := config()
			cfg.LocalNAT = 3
			m, _ := New(cfg)
			m.started, m.state, m.selection, m.selected = true, Connecting, true, UDP
			m.material = material()
			m.add(endpoint{link: Link{Address: netip.MustParseAddrPort("192.0.2.1:10001")}, connected: true, stage: precheckReady, peerNAT: 3})
			m.startRendezvous(0)
			addr := netip.MustParseAddrPort("198.51.100.1:40000")
			c := Control{Nonce: m.material.P2PNonce, RID: m.material.RID, WANCount: 1}
			c.WAN[0] = addr
			m.punchResult(0, operation{typ: 4}, c)
			if shared {
				m.add(endpoint{link: Link{Address: netip.MustParseAddrPort("192.0.2.2:10001")}, connected: true, stage: precheckReady, peerNAT: 0})
				m.startRendezvous(1)
				m.punchResult(1, operation{typ: 4}, c)
			}
			actions := m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(400*time.Millisecond))
			knocks := 0
			for _, a := range actions {
				if a.Kind == Send && a.Link.Address == addr {
					knocks++
				}
			}
			if (knocks > 0) != shared || m.hasLiveCandidates() != shared {
				t.Fatalf("expired candidate: knocks=%d live=%v, shared live task=%v", knocks, m.hasLiveCandidates(), shared)
			}
			if m.state != Connecting {
				t.Fatal("candidate expiry lost live relay")
			}
			m.peerKnock(Link{Address: addr}, knock(0x0402, m.cfg.UID, m.material.RID))
			if (m.state == Established) != shared {
				t.Fatal("late knock accepted without a live direct task")
			}
		})
	}
}
