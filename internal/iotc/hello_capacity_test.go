package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"testing"
	"time"
)

func TestFreshHelloCapacityAndDeadline(t *testing.T) {
	m, l, first := freshHello(t, 1, 7)
	serials := map[uint32]bool{le32(first.Body): true}
	for i := 1; i <= 119; i++ {
		out := m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(time.Duration(i)*500*time.Millisecond))
		sent := 0
		for _, a := range out {
			if a.Kind == Send && a.Link == l {
				q, e := DecodeLegacy(a.Data, m.key)
				if e == nil && q.Opcode == 0x8003 {
					sent++
					s := le32(q.Body)
					if serials[s] {
						t.Fatal("duplicate serial")
					}
					serials[s] = true
				}
			}
		}
		if sent != 1 {
			t.Fatalf("retry %d: sends=%d want 1", i, sent)
		}
	}
	if len(serials) != 120 {
		t.Fatalf("serials=%d want 120", len(serials))
	}
	i := m.find(l)
	if m.endpoints[i].helloCount != 119 {
		t.Fatal("incorrect history count")
	}
	for s := range serials {
		if !m.endpoints[i].ownsHello(s) {
			t.Fatalf("evicted serial %d", s)
		}
	}
	out := m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(60*time.Second))
	for _, a := range out {
		if a.Kind == Send {
			t.Fatal("sent at deadline")
		}
	}
	if m.State() != Closed || m.count != 0 {
		t.Fatal("deadline retained attempt")
	}
}

// A finite outage ends in time for the final hello flight and remaining
// handshake. Tests continued emission, not just retention of an old serial.
func TestFreshLastHelloRetryProgress(t *testing.T) {
	h := newHarness(t, "direct", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	h.step(Event{Kind: Start})
	hellos := 0
	runFiltered(h, 59999*time.Millisecond, func(p sim.Packet, _ core.Instant) bool {
		q, e := DecodeLegacy(p.Data, h.m.key)
		if e == nil && q.Opcode == 0x8003 {
			hellos++
			return hellos < 120
		}
		return false
	})
	if hellos != 120 || h.ready != 1 {
		t.Fatalf("hellos=%d Ready=%d want 120,1 after finite hello loss", hellos, h.ready)
	}
}
