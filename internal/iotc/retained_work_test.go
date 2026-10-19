package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"net/netip"
	"testing"
	"time"
)

func TestFreshSoundRetiredUDPDecryptFailure(t *testing.T) {
	h := newHarness(t, "silent", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	h.run(time.Second)
	server := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	h.encrypted(server, 4, h.fixtures[0x030d])
	h.run(time.Millisecond)
	h.hold = true
	h.encrypted(server, 4, h.fixtures[0x030d])
	h.run(20 * time.Millisecond)
	if h.ready != 1 || len(h.held) != 1 {
		t.Fatalf("setup ready=%d held=%d", h.ready, len(h.held))
	}
	a := h.held[0]
	h.step(Event{Kind: EffectFailed, Token: a.Token, Link: a.Link})
	if h.m.state != Established {
		t.Fatalf("retired UDP decrypt failure killed direct path: got %v want %v", h.m.state, Established)
	}
}

func TestFreshSoundDelayedRelayACK(t *testing.T) {
	h := newHarness(t, "silent", 0)
	m, _ := New(config())
	m.started = true
	m.state = Connecting
	m.material = material()
	m.selection = true
	m.selected = TCP
	l := Link{Transport: TCP, Address: netip.MustParseAddrPort("203.0.113.1:443"), ID: 10}
	idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
	m.cache.Entries[idx].Key = 3
	m.cache.Entries[idx].CachedMarker = Marker{10, 1}
	m.add(endpoint{link: l, cache: idx, connected: true, stage: relayInfoPending, sid: 0x55})
	b := h.fixtures[0x030e]
	m.pending[10] = operation{token: 100, kind: Open, link: l, typ: 4, sid: 0x55, size: len(b)}
	out := m.Step(Event{Kind: Decrypted, Attempt: 1, Token: 100, Data: b}, 0)
	var ack Action
	for _, a := range out {
		if a.Kind == Seal {
			ack = a
		}
	}
	if ack.Token == 0 {
		t.Fatal("no ACK operation")
	}
	m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(10*time.Millisecond))
	if m.state != Established {
		t.Fatal("setup not established")
	}
	out = m.Step(Event{Kind: Encrypted, Attempt: 1, Token: ack.Token, Data: make([]byte, len(ack.Data)+16)}, core.Instant(11*time.Millisecond))
	for _, a := range out {
		if a.Kind == Send && a.Link == l {
			return
		}
	}
	t.Fatal("selected relay ACK completion was discarded after Ready")
}

func TestFreshSoundPunchRecovery(t *testing.T) {
	h := newHarness(t, "direct", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	h.run(time.Second)
	if h.ready != 1 {
		t.Fatal("setup not ready")
	}
	count := func() int {
		n := 0
		for _, s := range h.trace {
			if s == "0403" {
				n++
			}
		}
		return n
	}
	before := count()
	server := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	h.encrypted(server, 4, h.fixtures[0x030d])
	h.run(10 * time.Millisecond)
	if count() <= before {
		t.Fatal("authenticated repeated punch did not resend knock on established direct path")
	}
	if h.ready != 1 {
		t.Fatal("recovery emitted another Ready")
	}
}

// Ready may precede asynchronous ACK sealing, but the peer must actually receive
// the selected relay ACK before it will forward data.
func TestSimulatorDelayedRelayACK(t *testing.T) {
	for _, mode := range []string{"relay", "tcp"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, mode, 0)
			h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
			h.holdRelayACK = true
			h.step(Event{Kind: Start})
			h.run(6 * time.Second)
			if h.ready != 1 || len(h.heldSeals) == 0 || h.relayACK[h.m.path] {
				t.Fatalf("setup ready=%d held ACKs=%d peer ACK=%t", h.ready, len(h.heldSeals), h.relayACK[h.m.path])
			}
			h.holdRelayACK = false
			for _, a := range h.heldSeals {
				h.seal(a)
			}
			h.heldSeals = nil
			h.run(10 * time.Millisecond)
			if !h.relayACK[h.m.path] {
				t.Fatal("selected ACK was not received after delayed Seal completion")
			}
			h.step(Event{Kind: Write, Data: []byte{1}})
			h.run(10 * time.Millisecond)
			if h.received != 1 {
				t.Fatalf("ACKed relay forwarded %d packets, want 1", h.received)
			}
		})
	}
}
