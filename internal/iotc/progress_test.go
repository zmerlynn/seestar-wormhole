package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"net/netip"
	"testing"
	"time"
)

func TestRelayTimeoutStages(t *testing.T) {
	for _, tc := range []struct {
		p    RelayProgress
		want string
	}{
		{RelayProgress{}, "connection timeout"},
		{RelayProgress{Phase: RelayRequesting, Requests: 4}, "relay result never arrived after 4 requests; connection timeout"},
		{RelayProgress{Phase: RelayRequesting, Requests: 4, RefusedResults: 2}, "relay requested; no usable result after 4 requests; connection timeout"},
		{RelayProgress{Phase: RelayPending, Requests: 4}, "relay pending; relay never became ready after 4 requests; connection timeout"},
		{RelayProgress{Phase: RelayAccepted}, "relay accepted; relay info request never sent; connection timeout"},
		{RelayProgress{Phase: RelayInfoOutstanding, InfoRequests: 118}, "relay accepted; relay info never arrived after 118 requests; connection timeout"},
		{RelayProgress{Phase: RelayInfoReceived}, "relay info received; relay selection did not complete; connection timeout"},
	} {
		if got := tc.p.Diagnostic(ConnectionTimeout); got != tc.want {
			t.Errorf("phase %v diagnostic = %q, want %q", tc.p.Phase, got, tc.want)
		}
	}
}

func TestRelayProgressRejectsUncorrelatedAndSaturates(t *testing.T) {
	h := newHarness(t, "silent", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	h.run(900 * time.Millisecond)
	l := h.links[linkName(Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")})]
	before := h.m.RelayProgress()
	raw := append([]byte(nil), h.fixtures[0x020d]...)
	raw[40] ^= 1 // Incorrect RID despite authenticated envelope.
	h.encrypted(l, 4, raw)
	h.run(10 * time.Millisecond)
	if got := h.m.RelayProgress(); got != before {
		t.Fatalf("uncorrelated result changed progress: %+v -> %+v", before, got)
	}
	v := &h.m.endpoints[h.m.find(l)]
	for i := 0; i < 70000; i++ {
		h.m.observeRelayResult(v, -7)
		h.m.observeRelayResult(v, 0)
		h.m.observeRelayResult(v, -2)
		h.m.relaySend(operation{link: l, opcode: 0x020c})
		h.m.relaySend(operation{link: l, opcode: 0x0241})
	}
	p := h.m.RelayProgress()
	if p.Requests != 65535 || p.InfoRequests != 65535 || p.PendingResults != 65535 || p.ReadyResults != 65535 || p.RefusedResults != 65535 || p.ServerRefusedResults != 65535 || p.RefusedPaths != 1 {
		t.Fatalf("saturated counters = %+v", p)
	}
	before = p
	h.step(Event{Kind: Cancel})
	h.step(Event{Kind: Decrypted, Token: 1, Data: h.fixtures[0x020d]})
	if got := h.m.RelayProgress(); got != before {
		t.Fatalf("closed/stale event changed snapshot: %+v -> %+v", before, got)
	}
}
