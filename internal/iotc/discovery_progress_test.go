package iotc

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/edge"
	"math/rand/v2"
	"net/netip"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

func TestHighLatencyDiscoveryProgress(t *testing.T) {
	for _, delay := range []time.Duration{100 * time.Millisecond, 300 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			h := newHarness(t, "direct", 0)
			h.impair = sim.Link{MTU: 1416, Delay: delay}
			h.step(Event{Kind: Start})
			h.run(61 * time.Second)
			if h.ready != 1 {
				t.Fatalf("healthy delayed path: one-way=%v Ready=%d state=%v disposed=%d trace=%v, want Ready=1", delay, h.ready, h.m.State(), h.disposed, h.trace)
			}
		})
	}
}

// No preinstalled endpoints: exercise DNS, master, hello and precheck before
// rendezvous. Only bounded first-flight loss; the peer is eventually responsive.
func runDiscoveryProgressSeed(t *testing.T, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, 404))
	delay := time.Duration(25+rng.IntN(1976)) * time.Millisecond
	mode := "direct"
	if seed%3 == 0 {
		mode = "relay"
	}
	h := newHarness(t, mode, 0)
	var previous sim.Packet
	for attempt := uint64(1); attempt <= 4; attempt++ {
		if attempt > 1 {
			// Reuse the owner, simulated host, UDP endpoint and network scheduler.
			// Machine is single-use: replacing it is the production redial contract.
			network := h.net
			*h = *newHarness(t, mode, 0)
			h.net = network
		}
		h.discoveryMaterial(rng, attempt)
		if seed%4 == 0 {
			// Alternate-source observation means NAT 1. Inner send layouts:
			// 0240 H+108 and 0241 H+56; retain all other literal fixture bytes.
			h.requests[0x0240][108] = 1
			h.requests[0x0241][56] = 1
		}
		h.impair = sim.Link{MTU: 1416, Delay: delay}
		h.step(Event{Kind: Start})
		staleDelivered := 0

		var retained sim.Packet
		droppedRequest, droppedReply := false, false
		infos := 0
		runFiltered(h, 30*time.Second, func(p sim.Packet, at core.Instant) bool {
			if attempt > 1 && p.To == "client" && bytes.Equal(p.Data, previous.Data) {
				staleDelivered++
				before := h.m.nat
				source := h.links[p.From]
				if seed%4 == 0 {
					source.Address = netip.AddrPortFrom(source.Address.Addr(), source.Address.Port()+1)
				}
				out := h.m.Step(Event{Kind: Wire, Attempt: attempt, Link: source, Data: p.Data}, at)
				for _, a := range out {
					if a.Kind != ArmTimer {
						t.Fatalf("seed=%d attempt=%d old reply emitted %v", seed, attempt, a.Kind)
					}
				}
				if before != h.m.nat {
					t.Fatalf("seed=%d attempt=%d old reply changed NAT", seed, attempt)
				}
				h.actions(out)
				return true
			}
			if mode == "relay" && p.To != "client" && len(p.Data) >= 12 && le32(p.Data) == 0xcc51 {
				plain, err := h.requestPlain(h.links[p.To], p.Data)
				if err != nil {
					t.Fatal(err)
				}
				if le16(plain[8:]) == 0x0241 {
					infos++
					if infos == 1 {
						return true
					}
				}
			}
			q, err := DecodeLegacy(p.Data, h.m.key)
			if err != nil {
				return false
			}
			if q.Opcode == 0x8003 && !droppedRequest {
				droppedRequest = true
				return true
			}
			if q.Opcode == 0x8004 && !droppedReply {
				droppedReply = true
				retained = sim.Packet{From: p.From, To: p.To, Data: append([]byte(nil), p.Data...)}
				if seed%4 == 0 {
					q.Opcode = 0x8005
					retained.Data, _ = EncodeLegacy(q, h.m.key)
				}
				// Delay this reply across two further retries. Later replies can overtake it.
				if seed%2 == 0 {
					if err := h.net.Send(p, sim.Link{MTU: 1416, Delay: 1200 * time.Millisecond}); err != nil {
						t.Fatal(err)
					}
				}
				return true
			}
			if q.Opcode == 0x8004 && seed%4 == 0 {
				// A master-advertised IP may answer 8005 from a different port.
				// Keep the old serial, including when the deferred reply is reordered.
				q.Opcode = 0x8005
				data, err := EncodeLegacy(q, h.m.key)
				if err != nil {
					t.Fatal(err)
				}
				l := h.links[p.From]
				l.Address = netip.AddrPortFrom(l.Address.Addr(), l.Address.Port()+1)
				h.step(Event{Kind: Wire, Link: l, Data: data})
				return true
			}
			return false
		})
		if mode == "relay" && infos < 2 {
			t.Fatalf("seed=%d relay info sends=%d want >=2 after first-flight loss", seed, infos)
		}
		if !droppedRequest || !droppedReply || h.ready != 1 {
			t.Fatalf("seed=%d RTT=%v request loss=%v reply loss/reorder=%v Ready=%d want true,true,1", seed, 2*delay, droppedRequest, droppedReply, h.ready)
		}
		h.step(Event{Kind: Write, Data: []byte{0x71}})
		h.run(2*delay + time.Second)
		if h.received != 1 {
			t.Fatalf("seed=%d attempt=%d data receipts=%d want 1", seed, attempt, h.received)
		}
		if attempt > 1 && staleDelivered != 2 {
			t.Fatalf("delayed previous replies=%d, want 2", staleDelivered)
		}
		if len(retained.Data) == 0 {
			t.Fatal("no reply retained for next attempt")
		}
		previous = retained
		h.step(Event{Kind: Cancel})
		if attempt < 4 {
			// Queue actual retained bytes before replacing the owner. They stay
			// in flight across teardown and arrive before/after the next retry.
			for _, offset := range []time.Duration{250 * time.Millisecond, 750 * time.Millisecond} {
				if err := h.net.Send(previous, sim.Link{MTU: 1416, Delay: 4*delay + offset}); err != nil {
					t.Fatal(err)
				}
			}
		}
		// Drain ordinary cleanup traffic; the deferred replies remain queued.
		runFiltered(h, 2*delay, func(sim.Packet, core.Instant) bool { return true })
	}
}

// Preserve independent literal packet layouts, substituting only fresh attempt
// identities at their documented offsets. No expected packet uses a builder.
func (h *harness) discoveryMaterial(rng *rand.Rand, attempt uint64) {
	cfg := config()
	cfg.Attempt = attempt
	var err error
	h.m, err = New(cfg)
	if err != nil {
		h.t.Fatal(err)
	}
	var random [4]byte
	put32(random[:], rng.Uint32())
	h.mat.HelloSerial, err = edge.HelloSerialStart(bytes.NewReader(random[:]))
	if err != nil {
		h.t.Fatal(err)
	}
	oldRID := h.mat.RID
	for i := range h.mat.RID {
		h.mat.RID[i] = byte(rng.Uint32())
	}
	for i := range h.mat.MasterKey {
		h.mat.MasterKey[i] = byte(rng.Uint32())
	}
	for i := range h.mat.MasterIV {
		h.mat.MasterIV[i] = byte(rng.Uint32())
	}
	h.mat.MasterNonce = uint16(1 + rng.IntN(65534))
	h.mat.P2PNonce = h.mat.MasterNonce + 1
	h.install(1, h.mat.MasterKey, h.mat.MasterIV)
	h.clientPrivate = testPrivate(h.t, byte(attempt+2))
	der, err := x509.MarshalPKIXPublicKey(h.clientPrivate.PublicKey())
	if err != nil {
		h.t.Fatal(err)
	}
	copy(h.mat.Public[:], der)
	for _, fixtures := range []map[uint16][]byte{h.requests, h.fixtures} {
		for op, raw := range fixtures {
			fixtures[op] = bytes.ReplaceAll(raw, oldRID[:], h.mat.RID[:])
		}
	}
	put16(h.requests[0x100b][16:], h.mat.MasterNonce)
	copy(h.requests[0x100b][20:36], h.mat.MasterKey[:])
	copy(h.requests[0x100b][36:48], h.mat.MasterIV[:])
	put16(h.requests[0x0217][16:], h.mat.P2PNonce)
	put16(h.fixtures[0x100c][16:], h.mat.MasterNonce)
	put16(h.fixtures[0x030d][16:], h.mat.P2PNonce)
}

// Retains real GCM and the independent peer; a filter can lose or defer an
// individual packet without changing production or inspecting endpoint stages.
func runFiltered(h *harness, d time.Duration, drop func(sim.Packet, core.Instant) bool) {
	end := h.net.Scheduler.Now().Add(d)
	for h.net.Scheduler.Now() < end {
		until := min(end, h.net.Scheduler.Now().Add(time.Millisecond))
		if err := h.net.Scheduler.Run(until, 10000, func(p sim.Packet, at core.Instant) error {
			if drop(p, at) {
				return nil
			}
			if p.To == "client" {
				h.step(Event{Kind: Wire, Link: h.links[p.From], Data: p.Data, Marker: Marker{100, 123}})
			} else {
				h.server(h.links[p.To], p.Data)
			}
			return nil
		}); err != nil {
			h.t.Fatal(err)
		}
		if h.timer != 0 && h.net.Scheduler.Now() >= h.timer {
			h.step(Event{Kind: Tick})
		}
	}
}

func TestHelloReplyAcrossRetries(t *testing.T) {
	for _, alternate := range []bool{false, true} {
		for _, age := range []time.Duration{600 * time.Millisecond, 59500 * time.Millisecond} {
			t.Run(fmt.Sprintf("alternate=%v/age=%v", alternate, age), func(t *testing.T) {
				m, _ := New(config())
				m.started, m.state, m.material, m.hasMaterial = true, Connecting, material(), true
				l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
				idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
				m.cache.Entries[idx].Key, m.cache.Entries[idx].CachedMarker = 3, Marker{10, 1}
				m.add(endpoint{link: l, connected: true, cache: idx})
				var first Packet
				for at := 500 * time.Millisecond; at <= age; at += 500 * time.Millisecond {
					out := m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(at))
					for _, a := range out {
						if a.Kind == Send {
							q, err := DecodeLegacy(a.Data, m.key)
							if err == nil && q.Opcode == 0x8003 && first.Body == nil {
								first = q
							}
						}
					}
				}
				// Ensure even the short case crosses an actual retry boundary.
				m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(max(age, time.Second)))
				q := controlPacket(0x8004, 0x3f, 24)
				copy(q.Body, addressBytes(netip.MustParseAddrPort("198.51.100.1:40000")))
				copy(q.Body[16:], first.Body[:6])
				if alternate {
					q.Opcode = 0x8005
					l.Address = netip.MustParseAddrPort("203.0.113.1:10002")
				}
				b, _ := EncodeLegacy(q, m.key)
				out := m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: b}, core.Instant(max(age, time.Second)+time.Millisecond))
				for _, a := range out {
					if a.Kind == Seal && le16(a.Data[8:]) == 0x0217 {
						return
					}
				}
				t.Fatal("reply to oldest outstanding hello did not emit precheck")
			})
		}
	}
}
