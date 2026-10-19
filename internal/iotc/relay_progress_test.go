package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"testing"
	"time"
)

// Losing the first 0241 must still allow establishment and application
// delivery through the peer's relay ACK gate.
func TestRelayInfoRetryMakesProgress(t *testing.T) {
	h := newHarness(t, "relay", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	infos := 0
	runFiltered(h, 3*time.Second, func(p sim.Packet, _ core.Instant) bool {
		if p.To == "client" || len(p.Data) < 12 || le32(p.Data) != 0xcc51 {
			return false
		}
		plain, err := h.requestPlain(h.links[p.To], p.Data)
		if err != nil {
			t.Fatal(err)
		}
		if le16(plain[8:]) == 0x0241 {
			infos++
			return infos == 1
		}
		return false
	})
	if infos < 2 || h.ready != 1 {
		t.Fatalf("lost info request: info Sends=%d Ready=%d want >=2,1", infos, h.ready)
	}
	h.step(Event{Kind: Write, Data: []byte{0x71}})
	h.run(20 * time.Millisecond)
	if h.received != 1 {
		t.Fatalf("payload receipts=%d want 1 after relay ACK", h.received)
	}
}
