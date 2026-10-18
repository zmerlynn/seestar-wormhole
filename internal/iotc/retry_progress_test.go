package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"net/netip"
	"testing"
	"time"
)

// Lose relay-info requests at the simulated network, while allowing discovery,
// real GCM, the independent direct server, and all owner timers to run normally.
func runWithoutRelayInfo(h *harness, d time.Duration) {
	end := h.net.Scheduler.Now().Add(d)
	for h.net.Scheduler.Now() < end {
		until := min(end, h.net.Scheduler.Now().Add(time.Millisecond))
		err := h.net.Scheduler.Run(until, 10000, func(p sim.Packet, at core.Instant) error {
			if p.To == "client" {
				h.step(Event{Kind: Wire, Link: h.links[p.From], Data: p.Data, Marker: Marker{100, 123}})
			} else {
				l := h.links[p.To]
				if len(p.Data) >= 12 && le32(p.Data) == 0xcc51 {
					plain, err := h.requestPlain(l, p.Data)
					if err != nil {
						return err
					}
					if le16(plain[8:]) == 0x0241 {
						continueTrace := "dropped-0241"
						h.trace = append(h.trace, continueTrace)
						return nil
					}
				}
				h.server(l, p.Data)
			}
			return nil
		})
		if err != nil {
			h.t.Fatal(err)
		}
		if h.timer != 0 && h.net.Scheduler.Now() >= h.timer {
			h.step(Event{Kind: Tick})
		}
	}
}

func TestRelayAcceptanceRetainsDirectRetry(t *testing.T) {
	h := newHarness(t, "silent", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	h.run(time.Second) // Initial direct requests receive no punch.
	server := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	raw := append([]byte(nil), h.fixtures[0x020d]...)
	put32(raw[36:], 0)
	h.encrypted(server, 4, raw)
	runWithoutRelayInfo(h, 10*time.Millisecond)
	if i := h.m.find(server); i < 0 || h.m.endpoints[i].stage != relayInfoPending {
		t.Fatal("setup did not accept relay")
	}
	h.mode = "direct" // The direct server can now answer a retried 0240.
	runWithoutRelayInfo(h, 2*time.Second)
	if h.ready != 1 || !h.m.direct {
		t.Fatalf("accepted relay stopped independent direct recovery: ready=%d state=%v trace=%v", h.ready, h.m.state, h.trace)
	}
}

// Enter through Wire to obtain a real Open token; emulate the edge's successful
// authentication completion. No direct call to the transition under test.
func applyAuthenticatedRecord(t *testing.T, m *Machine, l Link, typ byte, raw []byte, now core.Instant) []Action {
	t.Helper()
	prefix := 0
	if typ == 4 {
		prefix = 4
	}
	header, err := RecordHeader(typ, m.cfg.RecordSID, prefix+len(raw)+16)
	if err != nil {
		t.Fatal(err)
	}
	if typ == 4 {
		header = append(header, 0x55, 0, 0, 0)
	}
	wire := append(header, make([]byte, len(raw)+16)...)
	out := m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: wire}, now)
	for _, a := range out {
		if a.Kind == Open {
			return m.Step(Event{Kind: Decrypted, Attempt: 1, Token: a.Token, Data: raw}, now)
		}
	}
	t.Fatalf("no Open for %v type %d state %v", l, typ, m.state)
	return nil
}
