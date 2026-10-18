package iotc

import (
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"net/netip"
	"testing"
	"time"
)

func TestLatePrecheckAlternatives(t *testing.T) {
	for _, transport := range []Transport{UDP, TCP} {
		for _, failFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/failFirst=%v", transport, failFirst), func(t *testing.T) {
				m, _ := New(config())
				m.started, m.state, m.material, m.hasMaterial = true, Connecting, material(), true
				links := []Link{{Transport: transport, Address: netip.MustParseAddrPort("203.0.113.1:443"), ID: 100}, {Transport: transport, Address: netip.MustParseAddrPort("203.0.113.2:443"), ID: 101}}
				for _, l := range links {
					idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
					m.cache.Entries[idx].Key, m.cache.Entries[idx].CachedMarker = 3, Marker{10, 1}
					m.add(endpoint{link: l, connected: true, stage: precheckPending, cache: idx})
				}
				pre := controlPacket(0x0218, 0x24, 56)
				put16(pre.Body, m.material.P2PNonce)
				put32(pre.Body[4:], 0x55)
				copy(pre.Body[8:], m.cfg.UID[:])
				raw, _ := pre.Marshal()
				applyAuthenticatedRecord(t, m, links[0], 3, raw, core.Instant(2*time.Second))
				m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(2*time.Second))
				if !m.selection || m.selected != transport {
					t.Fatal("setup did not select expected transport")
				}
				if failFirst {
					m.Step(Event{Kind: LinkFailed, Attempt: 1, Link: links[0]}, core.Instant(2100*time.Millisecond))
				}
				out := applyAuthenticatedRecord(t, m, links[1], 3, raw, core.Instant(2200*time.Millisecond))
				requests := 0
				for _, a := range out {
					if a.Kind == Seal && a.Link == links[1] && le16(a.Data[8:]) == 0x020c {
						requests++
					}
				}
				if requests != 1 {
					t.Fatalf("late precheck emitted %d initial relay requests, want 1", requests)
				}

				// Finish the initial effects so the later observation must be a retry.
				for _, a := range out {
					if a.Kind == Seal {
						m.Step(Event{Kind: Encrypted, Attempt: 1, Token: a.Token, Link: a.Link, Data: make([]byte, len(a.Data)+16)}, core.Instant(2200*time.Millisecond))
					}
				}
				if !failFirst {
					m.Step(Event{Kind: LinkFailed, Attempt: 1, Link: links[0]}, core.Instant(2300*time.Millisecond))
				}
				out = m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(5*time.Second))
				for _, a := range out {
					if a.Kind == Seal && a.Link == links[1] && le16(a.Data[8:]) == 0x020c {
						sent := m.Step(Event{Kind: Encrypted, Attempt: 1, Token: a.Token, Link: a.Link, Data: make([]byte, len(a.Data)+16)}, core.Instant(5*time.Second))
						for _, b := range sent {
							if b.Kind == Send && b.Link == links[1] {
								return
							}
						}
					}
				}
				t.Fatalf("late healthy alternative stranded: state=%v stage=%v want relay request on %v", m.state, m.endpoints[1].stage, links[1])
			})
		}
	}

}
