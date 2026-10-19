package iotc

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// Real late hello admission must not rewrite a task's construction-time budget.
func TestDirectTaskSurvivesNATReclassification(t *testing.T) {
	for _, tc := range []struct {
		local, peer, observed byte
		alternate             bool
	}{{3, 3, 1, true}, {3, 3, 2, false}, {2, 3, 3, false}, {3, 5, 1, true}} {
		t.Run(fmt.Sprintf("%d+%d/to=%d", tc.local, tc.peer, tc.observed), func(t *testing.T) {
			cfg := config()
			cfg.LocalNAT, cfg.P2PPortCount = tc.local, 4
			m, _ := New(cfg)
			m.started, m.state, m.material, m.selection, m.selected = true, Connecting, material(), true, UDP
			l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
			idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
			m.cache.Entries[idx].Key, m.cache.Entries[idx].CachedMarker = 3, Marker{10, 1}
			m.add(endpoint{link: l, connected: true, cache: idx, hello: 77, peerNAT: tc.peer, stage: precheckReady})
			m.startRendezvous(0)
			m.pending = [64]operation{}
			observed := netip.MustParseAddrPort("198.51.100.1:40000")
			prior := observed
			if tc.observed == 3 {
				prior = netip.MustParseAddrPort("198.51.100.1:40001")
			}
			m.nat.Observe(netip.MustParseAddrPort("203.0.113.2:10001"), prior, false)
			op := uint16(0x8004)
			if tc.alternate {
				op = 0x8005
			}
			q := controlPacket(op, 0x3f, 24)
			copy(q.Body, addressBytes(observed))
			put32(q.Body[16:], 77)
			put16(q.Body[20:], uint16(cfg.RecordSID))
			b, _ := EncodeLegacy(q, m.key)
			m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: b}, core.Instant(100*time.Millisecond))
			if m.cfg.LocalNAT != tc.observed {
				t.Fatalf("fixture NAT=%d, want %d", m.cfg.LocalNAT, tc.observed)
			}
			sumSix := int(tc.local)+int(tc.peer) == 6
			count := func(at time.Duration) int {
				n := 0
				for _, a := range m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(at)) {
					if a.Kind == Seal && le16(a.Data[8:]) == 0x0240 {
						n++
					}
				}
				m.pending = [64]operation{}
				return n
			}
			want := 1
			if sumSix {
				want = 0
			}
			if got := count(150 * time.Millisecond); got != want {
				t.Errorf("creation-time cadence: requests=%d, want %d", got, want)
			}
			candidate := netip.MustParseAddrPort("198.51.100.9:41000")
			c := Control{Nonce: m.material.P2PNonce, RID: m.material.RID, WANCount: 1}
			c.WAN[0] = candidate
			m.punchResult(0, operation{typ: 4}, c)
			if got := count(400 * time.Millisecond); got != want || m.endpoints[0].directExpired != sumSix {
				t.Errorf("creation-time lifetime: requests=%d expired=%v, want %d/%v", got, m.endpoints[0].directExpired, want, sumSix)
			}
			if m.hasLiveCandidates() == sumSix {
				t.Errorf("candidate authority: live=%v, want %v", m.hasLiveCandidates(), !sumSix)
			}
			m.peerKnock(Link{Address: candidate}, knock(0x0402, m.cfg.UID, m.material.RID))
			if (m.state == Established) == sumSix {
				t.Fatal("late knock used reclassified task lifetime")
			}
		})
	}
}
