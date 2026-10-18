package iotc

import (
	"bytes"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

// Observed direct selection despite destination-dependent mappings is scripted
// here, without promising a NAT outcome. Direct work continues alongside relay
// work. This models one live attempt; the observations used three allocations.
func TestObservedSymmetricNATEventuallyDirect(t *testing.T) {
	h := newHarness(t, "direct", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	// Advertise a second modern server with the same public test key. Retain
	// the literal master layout, changing only lengths and server address.
	master := bytes.Clone(h.fixtures[0x100c])
	entry := bytes.Clone(master[90:])
	entry[7] = 2 // 203.0.113.2, versus 203.0.113.1
	master = append(master, entry...)
	put16(master[4:], uint16(len(master)-16))
	put16(master[88:], 216)
	h.fixtures[0x100c] = master
	h.requests[0x0240][108] = 3 // literal NAT field at H+108
	h.step(Event{Kind: Start})
	var helloReplies, lostKnocks int
	runFiltered(h, 250*time.Millisecond, func(p sim.Packet, at core.Instant) bool {
		// Finish both mapping observations before rendezvous starts.
		if p.To == "client" && len(p.Data) >= 12 && le32(p.Data) == 0xcc51 && p.Data[5] == 3 && helloReplies < 2 {
			if err := h.net.Send(p, sim.Link{MTU: 1416, Delay: time.Millisecond}); err != nil {
				t.Fatal(err)
			}
			return true
		}
		q, err := DecodeLegacy(p.Data, h.m.key)
		if err != nil {
			return false
		}
		if p.To == "client" && q.Opcode == 0x8004 {
			helloReplies++
			// Same public IPv4, destination-dependent external port. Preserve
			// the issued serial and SID echoed by the ordinary peer.
			if h.links[p.From].Address.Addr().String() == "203.0.113.2" {
				q.Body[2], q.Body[3] = 0x9c, 0x41 // 40001, versus 40000
			}
			wire, err := EncodeLegacy(q, h.m.key)
			if err != nil {
				t.Fatal(err)
			}
			h.step(Event{Kind: Wire, Link: h.links[p.From], Data: wire})
			return true
		}
		if p.To != "client" && q.Opcode == 0x0403 && at < core.Instant(200*time.Millisecond) {
			lostKnocks++
			return true
		}
		return false
	})
	if helloReplies != 2 || h.m.nat.Type() != 3 || h.m.cfg.LocalNAT != 3 || lostKnocks == 0 {
		t.Fatalf("script: hellos=%d NAT=%d/%d lost knocks=%d; want 2,3/3,positive", helloReplies, h.m.nat.Type(), h.m.cfg.LocalNAT, lostKnocks)
	}
	if h.ready != 1 || h.m.State() != Established || !h.m.direct || h.m.path.Transport != UDP || h.disposed != 0 || len(h.failures) != 0 {
		t.Fatalf("late direct: ready=%d state=%v direct=%v path=%v disposed=%d failures=%v", h.ready, h.m.State(), h.m.direct, h.m.path, h.disposed, h.failures)
	}
	h.step(Event{Kind: Write, Data: []byte{0x00, 0xff, 0x0d, 0x0a}})
	h.run(10 * time.Millisecond)
	if h.received != 1 || h.trace[len(h.trace)-1] != "data:00ff0d0a" {
		t.Fatalf("selected direct bytes: received=%d trace=%v", h.received, h.trace)
	}
	h.step(Event{Kind: Cancel})
	if h.disposed != 1 {
		t.Fatalf("attempt disposals=%d, want 1", h.disposed)
	}
}

// Observed after rebinding: the device continued sending to the old mapping.
// Unanswered selected-route liveness expires; migration requires packets to
// actually reach the peer.
func TestObservedMappingStaysStale(t *testing.T) {
	h := newHarness(t, "direct", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	h.run(20 * time.Millisecond)
	if h.ready != 1 {
		t.Fatalf("initial ready=%d, want 1", h.ready)
	}
	path := h.m.path
	// The new outbound mapping cannot reach the peer. Independently, peer
	// heartbeats keep targeting the old mapping and never reach this client.
	// An emitted 0427 is not evidence of delivery: address migration would
	// require a matching alive to reach the device.
	q, err := ParsePacket(unhex("04021d000800000028041200000000004433221188776655"))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := EncodeLegacy(q, h.m.key)
	if err != nil {
		t.Fatal(err)
	}
	for delay := time.Second; delay <= 32*time.Second; delay += time.Second {
		if err := h.net.Send(sim.Packet{From: linkName(path), To: "client", Data: wire}, sim.Link{MTU: 1416, Delay: delay}); err != nil {
			t.Fatal(err)
		}
	}
	var outbound, staleReplies, negotiation int
	stale := func(p sim.Packet, _ core.Instant) bool {
		q, err := DecodeLegacy(p.Data, h.m.key)
		if p.To == "client" {
			if err == nil && q.Opcode == 0x0428 {
				staleReplies++
			}
			return true // Peer replies still target the unreachable old mapping.
		}
		if err == nil && q.Opcode == 0x0427 {
			outbound++
			if h.links[p.To] != path || !bytes.Equal(q.Body, []byte{0x44, 0x33, 0x22, 0x11, 0x88, 0x77, 0x66, 0x55}) {
				t.Fatalf("selected-route alive path=%v body=%x", h.links[p.To], q.Body)
			}
			return true // New mapping's alive never arrives at the device.
		} else if len(p.Data) >= 4 && le32(p.Data) == 0xcc51 {
			negotiation++
		}
		return false
	}
	runFiltered(h, 30*time.Second, stale)
	if h.m.State() != Established || h.disposed != 0 || len(h.failures) != 0 {
		t.Fatalf("expired early: state=%v disposed=%d failures=%v", h.m.State(), h.disposed, h.failures)
	}
	runFiltered(h, 2*time.Second, stale)
	if outbound == 0 || staleReplies == 0 || negotiation != 0 || h.ready != 1 || h.disposed != 1 || len(h.failures) != 1 || h.failures[0].Reason != "liveness timeout" {
		t.Fatalf("stale mapping: alive=%d old replies=%d negotiation=%d ready=%d disposed=%d failures=%v", outbound, staleReplies, negotiation, h.ready, h.disposed, h.failures)
	}
	runFiltered(h, time.Minute, stale)
	if h.ready != 1 || h.disposed != 1 || len(h.failures) != 1 {
		t.Fatalf("terminal attempt revived: ready=%d disposed=%d failures=%v", h.ready, h.disposed, h.failures)
	}
}
