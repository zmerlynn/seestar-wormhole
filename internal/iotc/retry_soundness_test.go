package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"net/netip"
	"testing"
	"time"
)

func TestSoundKnockRetry(t *testing.T) {
	h := newHarness(t, "silent", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	h.run(time.Second)
	server := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	h.encrypted(server, 4, h.fixtures[0x030d]) // Exactly one authenticated punch.
	dropped := map[Link]bool{}
	end := h.net.Scheduler.Now().Add(time.Second)
	for h.net.Scheduler.Now() < end {
		until := min(end, h.net.Scheduler.Now().Add(time.Millisecond))
		err := h.net.Scheduler.Run(until, 10000, func(p sim.Packet, at core.Instant) error {
			if p.To == "client" {
				h.step(Event{Kind: Wire, Link: h.links[p.From], Data: p.Data, Marker: Marker{100, 123}})
				return nil
			}
			l := h.links[p.To]
			if q, err := DecodeLegacy(p.Data, h.m.key); err == nil && q.Opcode == 0x0403 && !dropped[l] {
				dropped[l] = true
				return nil
			}
			h.server(l, p.Data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if h.timer != 0 && h.net.Scheduler.Now() >= h.timer {
			h.step(Event{Kind: Tick})
		}
	}
	if len(dropped) == 0 {
		t.Fatal("setup did not drop first knock flight")
	}
	if h.ready != 1 || !h.m.direct {
		t.Fatalf("lost first knock flight did not recover: ready=%d state=%v", h.ready, h.m.state)
	}
}

func TestRefusedPunchRecovery(t *testing.T) {
	h := newHarness(t, "silent", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	h.run(time.Second)
	server := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	raw := append([]byte(nil), h.fixtures[0x020d]...)
	put32(raw[36:], 0xfffffffb)
	h.encrypted(server, 4, raw)
	h.run(10 * time.Millisecond)
	h.encrypted(server, 4, h.fixtures[0x030d])
	h.run(20 * time.Millisecond)
	if h.ready != 1 || !h.m.direct {
		t.Fatal("setup did not select direct path after refusal")
	}
	before := len(h.trace)
	h.encrypted(server, 4, h.fixtures[0x030d])
	h.run(10 * time.Millisecond)
	for _, op := range h.trace[before:] {
		if op == "0403" {
			return
		}
	}
	t.Fatal("relay-refused endpoint lost authenticated punch recovery after direct Ready")
}

func TestRefusedRelayEffect(t *testing.T) {
	for _, kind := range []EventKind{Encrypted, EffectFailed} {
		t.Run(map[EventKind]string{Encrypted: "complete", EffectFailed: "fail"}[kind], func(t *testing.T) {
			m, _ := New(config())
			m.started, m.state, m.material, m.hasMaterial = true, Connecting, material(), true
			m.selection, m.selected = true, UDP
			l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
			idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
			m.cache.Entries[idx].Key, m.cache.Entries[idx].CachedMarker = 3, Marker{10, 1}
			m.add(endpoint{link: l, connected: true, stage: precheckReady, cache: idx, sid: 0x55})
			m.startRendezvous(0)
			initial := m.out
			for _, a := range initial {
				if a.Kind == Seal {
					m.Step(Event{Kind: Encrypted, Attempt: 1, Token: a.Token, Data: make([]byte, len(a.Data)+16)}, 0)
				}
			}
			retry := m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(500*time.Millisecond))
			var pending Action
			for _, a := range retry {
				if a.Kind == Seal && le16(a.Data[8:]) == 0x020c {
					pending = a
				}
			}
			if pending.Token == 0 {
				t.Fatal("no pending relay request")
			}
			// A prior transmitted request can provoke a refusal while a retry is sealing.
			q := controlPacket(0x020d, 0x24, 32)
			copy(q.Body, m.cfg.UID[:])
			copy(q.Body[24:], m.material.RID[:])
			put32(q.Body[20:], 0xfffffffb)
			raw, _ := q.Marshal()
			applyAuthenticatedRecord(t, m, l, 4, raw, core.Instant(501*time.Millisecond))
			out := m.Step(Event{Kind: kind, Attempt: 1, Token: pending.Token, Link: pending.Link, Data: make([]byte, len(pending.Data)+16)}, core.Instant(502*time.Millisecond))
			if m.state != Connecting {
				t.Fatalf("retired relay effect killed owned direct negotiation: got=%v want=%v", m.state, Connecting)
			}
			for _, a := range out {
				if a.Kind == Send && a.Link == l {
					t.Fatal("retired relay request completed into a Send")
				}
			}
		})
	}
}
