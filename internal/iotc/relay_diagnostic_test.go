package iotc

import (
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

// The scenario uses synthetic documentation-range addresses and existing
// independent byte fixtures for relay results and timers.
// One server refuses, another goes pending then ready repeatedly; neither relay
// info nor direct knocks are answered. No wall clock or external I/O is used.
func runRelayInfoSilence(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, "silent", 0)
	h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
	raw := append([]byte(nil), h.fixtures[0x100c]...)
	second := append([]byte(nil), raw[90:198]...)
	copy(second[:16], unhex("02000d96cb0071020000000000000000")) // .2:3478
	raw = append(raw, second...)
	put16(raw[4:], uint16(len(raw)-16))
	put16(raw[88:], 216) // Two independent server/key entries in TDU 9.
	h.fixtures[0x100c] = raw
	h.step(Event{Kind: Start})
	requests := 0
	for h.net.Scheduler.Now() < core.Instant(61*time.Second) {
		until := h.net.Scheduler.Now().Add(time.Millisecond)
		err := h.net.Scheduler.Run(until, 10000, func(p sim.Packet, at core.Instant) error {
			if p.To == "client" {
				h.step(Event{Kind: Wire, Link: h.links[p.From], Data: p.Data, Marker: Marker{100, 123}})
				return nil
			}
			l := h.links[p.To]
			if len(p.Data) >= 12 && le32(p.Data) == 0xcc51 {
				plain, err := h.requestPlain(l, p.Data)
				if err != nil {
					return err
				}
				op := le16(plain[8:])
				if op == 0x020c || op == 0x0241 {
					h.trace = append(h.trace, "relay-request")
					result := uint32(0)
					if l.Address.Port() == 3478 {
						result = 0xfffffffe
					} else if op == 0x020c {
						requests++
						if requests == 1 {
							result = 0xfffffff9
						}
					}
					reply := append([]byte(nil), h.fixtures[0x020d]...)
					put32(reply[36:], result)
					h.encrypted(l, 4, reply)
					return nil // 0241 receives repeated ready, never 030e.
				}
				if op == 0x0240 {
					h.encrypted(l, 4, h.fixtures[0x030d])
					return nil
				}
			} else if q, err := DecodeLegacy(p.Data, h.m.key); err == nil && q.Opcode == 0x0403 {
				h.trace = append(h.trace, "unanswered-knock")
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
	return h
}

func TestSimulatorRelayInfoStallDiagnostic(t *testing.T) {
	h := runRelayInfoSilence(t)
	if h.ready != 0 || h.disposed != 1 || h.m.State() != Closed {
		t.Fatalf("ready=%d disposed=%d state=%v, want 0/1/closed", h.ready, h.disposed, h.m.State())
	}
	reason := h.trace[len(h.trace)-1]
	if !strings.Contains(reason, "relay accepted; relay info never arrived after ") || !strings.Contains(reason, "server refused") {
		t.Fatalf("timeout diagnostic = %q, want stalled relay-info stage and competing refusal", reason)
	}
	if strings.Count(strings.Join(h.trace, ","), "unanswered-knock") < 10 {
		t.Fatal("simulation did not continue unanswered direct knocks")
	}
}

func TestRelayProgressCountersAndMilestones(t *testing.T) {
	h := runRelayInfoSilence(t)
	p := h.m.RelayProgress()
	t.Logf("terminal relay snapshot: %+v", p)
	if p.Phase != RelayInfoOutstanding || p.Requests != 3 || p.PendingResults != 1 || p.ReadyResults < 100 || p.RefusedResults != 1 || p.ServerRefusedResults != 1 || p.RefusedPaths != 1 || p.InfoRequests < 100 || p.InfoOutstanding != 1 || p.InfoReceived != 0 || p.Selected {
		t.Fatalf("relay progress = %+v, want accepted relay awaiting info, retries and one competing refusal", p)
	}
	if len(h.failures) != 1 || h.failures[0].Progress != p || h.failures[0].Failures&ServerRefused == 0 || h.failures[0].Failures&ConnectionTimeout == 0 {
		t.Fatalf("terminal summary = %+v, want one final snapshot and original failure bits", h.failures)
	}
	want := []RelayPhase{RelayRequesting, RelayPending, RelayAccepted, RelayInfoOutstanding}
	if len(h.progress) != len(want) {
		t.Fatalf("phase events = %v, want %v", h.progress, want)
	}
	for i, phase := range want {
		if h.progress[i].Phase != phase {
			t.Fatalf("phase[%d] = %v, want %v", i, h.progress[i].Phase, phase)
		}
	}
}

func TestRelayProgressSuccessAndDirectRace(t *testing.T) {
	for _, mode := range []string{"relay", "tcp", "direct"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, mode, 0)
			h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
			h.step(Event{Kind: Start})
			h.run(10 * time.Second)
			p := h.m.RelayProgress()
			if h.ready != 1 {
				t.Fatal("simulation did not establish")
			}
			if mode == "direct" {
				if p.Selected {
					t.Fatalf("direct race reported relay selected: %+v", p)
				}
			} else if p.Phase != RelaySelected || !p.Selected || p.InfoReceived == 0 || p.InfoOutstanding != 0 {
				t.Fatalf("selected relay progress = %+v", p)
			}
			if h.readyProgress != p {
				t.Fatalf("Ready snapshot = %+v, want %+v", h.readyProgress, p)
			}
			seen := map[RelayPhase]bool{}
			for _, progress := range h.progress {
				if seen[progress.Phase] {
					t.Fatalf("duplicate phase event %v", progress.Phase)
				}
				seen[progress.Phase] = true
			}
		})
	}
}
