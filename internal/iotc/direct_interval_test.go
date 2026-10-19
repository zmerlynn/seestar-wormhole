package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"net/netip"
	"testing"
	"time"
)

func TestDirectRequestTaskInterval(t *testing.T) {
	for _, tc := range []struct {
		name               string
		ports, local, peer byte
		interval           time.Duration
	}{
		{"normal", 0, 0, 0, 200 * time.Millisecond}, {"three", 3, 0, 0, 200 * time.Millisecond},
		{"four", 4, 0, 0, 50 * time.Millisecond}, {"many", 32, 0, 0, 50 * time.Millisecond},
		{"NAT sum six", 4, 3, 3, 200 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config()
			cfg.P2PPortCount, cfg.LocalNAT = tc.ports, tc.local
			m, _ := New(cfg)
			m.started, m.state, m.material = true, Connecting, material()
			l := Link{Address: netip.MustParseAddrPort("192.0.2.1:10001")}
			idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
			m.cache.Entries[idx].Key = 3
			m.cache.Entries[idx].CachedMarker = Marker{10, 1}
			m.add(endpoint{link: l, connected: true, cache: idx, stage: precheckReady, peerNAT: tc.peer, sid: 0x55})
			m.startRendezvous(0)
			m.pending = [64]operation{}
			count := func(at time.Duration) int {
				n := 0
				for _, a := range m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(at)) {
					if a.Kind == Seal && le16(a.Data[8:]) == 0x0240 {
						n++
					}
				}
				return n
			}
			if n := count(tc.interval - time.Millisecond); n != 0 {
				t.Fatalf("early requests=%d", n)
			}
			if n := count(tc.interval); n != 1 {
				t.Fatalf("due requests=%d, want 1", n)
			}
		})
	}
}
