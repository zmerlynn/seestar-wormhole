package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"net/netip"
	"testing"
	"time"
)

func TestFreshRelayRefusalBeforePunch(t *testing.T) {
	h := newHarness(t, "silent", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	h.run(time.Second)
	l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	i := h.m.find(l)
	if i < 0 || h.m.endpoints[i].stage != rendezvousPending || h.m.candidateCount != 0 {
		t.Fatal("setup did not reach parallel rendezvous")
	}
	raw := append([]byte(nil), h.fixtures[0x020d]...)
	put32(raw[36:], uint32(0xfffffffb)) // -5: relay unsupported, not direct-path failure.
	h.encrypted(l, 4, raw)
	h.run(10 * time.Millisecond)
	if h.m.state != Connecting {
		t.Errorf("relay-only refusal closed outstanding direct negotiation: got state %v want Connecting (%v)", h.m.state, Connecting)
	}
	h.mode = "direct"
	h.encrypted(l, 4, h.fixtures[0x030d])
	h.run(100 * time.Millisecond)
	if h.ready != 1 || !h.m.direct {
		t.Errorf("valid delayed punch cannot establish: ready=%d direct=%t trace=%v", h.ready, h.m.direct, h.trace)
	}
}

func TestRelayRefusalRetainsDirectRetry(t *testing.T) {
	h := newHarness(t, "silent", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	h.run(time.Second)
	l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	raw := append([]byte(nil), h.fixtures[0x020d]...)
	put32(raw[36:], 0xfffffffb)
	h.encrypted(l, 4, raw)
	h.run(10 * time.Millisecond)
	before := len(h.trace)
	h.run(time.Second)
	direct := false
	for _, op := range h.trace[before:] {
		if op == "020c" || op == "0241" {
			t.Fatalf("refused relay retried %s", op)
		}
		direct = direct || op == "0240"
	}
	if !direct {
		t.Fatal("relay refusal lost direct retry")
	}
}
