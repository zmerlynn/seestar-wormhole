package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"net/netip"
	"testing"
	"time"
)

func TestFreshFailedDirectCandidate(t *testing.T) {
	h := newHarness(t, "silent", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	h.run(time.Second)
	server := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	h.encrypted(server, 4, h.fixtures[0x030d])
	// Punch and outgoing probes run; their replies remain queued for the next ms.
	h.run(2 * time.Millisecond)
	if h.m.state != Connecting || h.m.candidateCount == 0 {
		t.Fatal("setup did not reach direct probes")
	}
	// The edge contract reports rejected sends as LinkFailed. Fail every emitted
	// candidate so that a different successful candidate cannot mask the failure.
	for _, addr := range h.m.candidates[:h.m.candidateCount] {
		h.step(Event{Kind: LinkFailed, Link: Link{Address: addr}})
	}
	h.run(10 * time.Millisecond)
	if h.ready != 0 {
		t.Fatalf("failed direct candidate announced Ready: %v", h.m.path)
	}
}

func TestCandidateRetirementAndUnion(t *testing.T) {
	m := terminalMachine(t, 0)
	m.selected = UDP
	l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	m.add(endpoint{link: l, connected: true, stage: precheckReady})
	m.startRendezvous(0)
	first := netip.MustParseAddrPort("198.51.100.1:40000")
	second := netip.MustParseAddrPort("198.51.100.2:40000")
	punch := func(addr netip.AddrPort) {
		m.out = nil
		m.punchResult(0, operation{typ: 4}, Control{Nonce: m.material.P2PNonce, RID: m.material.RID, WAN: [8]netip.AddrPort{addr}, WANCount: 1})
	}
	punch(first)
	m.knockReply[0] = true
	m.Step(Event{Kind: LinkFailed, Attempt: 1, Link: Link{Address: first}}, 0)
	punch(first)
	for _, a := range m.out {
		if a.Kind == Send && a.Link.Address == first {
			t.Fatal("later punch resurrected retired candidate")
		}
	}
	punch(second)
	// A second server/update cannot erase the surviving candidate or its handshake.
	m.knockReply[m.candidateCount-1] = true
	punch(first)
	m.out = nil
	m.peerKnock(Link{Address: second}, knock(0x0404, m.cfg.UID, m.material.RID))
	if m.state != Established || !m.Direct() || m.path.Address != second {
		t.Fatal("later punch stranded another live candidate or erased its handshake")
	}
}

func TestCandidateFailureExhaustion(t *testing.T) {
	m := terminalMachine(t, 0)
	m.selected = UDP
	addr := netip.MustParseAddrPort("198.51.100.1:40000")
	m.candidates[0], m.candidateCount = addr, 1
	m.add(endpoint{stage: endpointRetired, failure: TransportFailure})
	m.Step(Event{Kind: LinkFailed, Attempt: 1, Link: Link{Address: addr}}, 0)
	if m.state != Closed {
		t.Fatal("last candidate failure left exhausted attempt connecting")
	}
}
