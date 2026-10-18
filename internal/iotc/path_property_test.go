package iotc

import (
	"fmt"
	"math/rand/v2"
	"net/netip"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

// The oracle tracks ownership independently of endpoint stages. Seeds 1..3
// cover refusal before punch, ready-relay replacement and failed candidate
// before ACK. Seeds 4..7 cover held-Open retirement,
// candidate-union handshake progress, refusal recovery and delayed relay ACK.
// Other seeds shuffle successes, refusals,
// failures and late completions across three servers and both transports.
func TestPathOwnershipSeeds(t *testing.T) {
	for seed := uint64(1); seed <= 128; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runOwnershipSeed(t, seed) })
	}
}

type pathInput struct {
	kind   string
	server int
}
type ownedServer struct {
	live, direct, relay, accepted, ready bool
	allocation                           uint16
}

func runOwnershipSeed(t *testing.T, seed uint64) {
	t.Helper()
	h := newHarness(t, "silent", 0)
	m, _ := New(config())
	m.started, m.hasMaterial, m.state, m.material = true, true, Connecting, material()
	transport := UDP
	if seed > 6 && seed%2 == 0 {
		transport = TCP
	}
	m.selection, m.selected = true, transport
	var links [3]Link
	var model [3]ownedServer
	for i := range links {
		links[i] = Link{Transport: transport, Address: netip.AddrPortFrom(netip.AddrFrom4([4]byte{203, 0, 113, byte(i + 1)}), 10001)}
		if transport == TCP {
			links[i].ID = uint64(i + 1)
		}
		idx, _ := m.cache.Install(Server{Address: links[i].Address}, Marker{10, 1})
		m.cache.Entries[idx].Key, m.cache.Entries[idx].CachedMarker = 3, Marker{10, 1}
		m.add(endpoint{link: links[i], connected: true, stage: precheckReady, cache: idx, sid: 0x55})
		m.startRendezvous(i)
		model[i] = ownedServer{live: true, direct: transport == UDP, relay: true, allocation: uint16(0x100 + i)}
	}
	// Finish every setup effect through the public completion contract.
	initial := m.out
	for _, a := range initial {
		if a.Kind == Seal {
			m.Step(Event{Kind: Encrypted, Attempt: 1, Token: a.Token, Data: make([]byte, len(a.Data)+16)}, 0)
		}
	}
	livePeers := map[netip.AddrPort]bool{}
	failedPeers := map[netip.AddrPort]bool{}
	var peerOrder []netip.AddrPort
	var held []Action
	var trace []pathInput
	var selected Link
	attemptFailed := false
	handshakes := map[netip.AddrPort]bool{}
	relayOwner := -1
	selectedFailed := false
	schedule := sim.New[pathInput](1<<20, 1024)
	rng := rand.New(rand.NewPCG(seed, 91))
	var inputs []pathInput
	switch seed {
	case 1:
		inputs = []pathInput{{"refuse", 0}, {"fail", 1}, {"fail", 2}, {"punch", 0}, {"peer", 0}}
	case 2:
		inputs = []pathInput{{"accept", 0}, {"info", 0}, {"complete", 0}, {"accept", 1}, {"info", 1}, {"complete", 0}, {"fail", 1}, {"tick", 0}}
	case 3:
		inputs = []pathInput{{"punch", 0}, {"failPeers", 0}, {"peer", 0}, {"punch", 1}, {"peer", 0}}
	case 4:
		inputs = []pathInput{{"punch", 0}, {"holdOpen", 0}, {"peer", 0}, {"failEffect", 0}}
	case 5:
		inputs = []pathInput{{"punch", 0}, {"peerHello", 0}, {"punch", 1}, {"peerACK", 0}}
	case 6:
		inputs = []pathInput{{"refuse", 0}, {"punch", 0}, {"peer", 0}, {"punch", 0}}
	case 7:
		inputs = []pathInput{{"accept", 0}, {"info", 0}, {"tick", 0}, {"complete", 0}}
	default:
		kinds := []string{"refuse", "accept", "info", "fail", "punch", "failPeers", "peer", "complete", "tick", "failPeer", "peerHello", "peerACK", "holdOpen", "rejectOpen", "failEffect"}
		for n := 0; n < 80; n++ {
			inputs = append(inputs, pathInput{kinds[rng.IntN(len(kinds))], rng.IntN(3)})
			if n == 79 && seed%3 == 0 {
				inputs = append(inputs, pathInput{"cancel", 0})
			}
		}
	}
	for n, e := range inputs {
		if err := schedule.Schedule(core.Instant(n+1)*core.Instant(time.Millisecond), e, 0); err != nil {
			t.Fatal(err)
		}
	}
	check := func(out []Action, input pathInput) {
		t.Helper()
		for _, a := range out {
			if a.Kind == Seal {
				held = append(held, a)
			}
			if a.Kind == Ready {
				if selected.Address.IsValid() {
					t.Fatalf("seed %d trace %v: duplicate Ready", seed, trace)
				}
				if failedPeers[a.Link.Address] {
					t.Fatalf("seed %d trace %v: Ready for failed direct candidate %v", seed, trace, a.Link)
				}
				if !livePeers[a.Link.Address] {
					found := false
					for i, l := range links {
						if a.Link == l && model[i].live && model[i].ready {
							found = true
							// Observe allocation ownership on the public packet path, rather
							// than reading the selector's internal allocation field.
							sends := m.Step(Event{Kind: Write, Attempt: 1, Data: []byte{0x73}}, schedule.Now())
							delivered := false
							for _, send := range sends {
								if send.Kind == Send && send.Link == l {
									p, err := DecodeLegacy(send.Data, ScrambleKey(config().License.Mask))
									if err != nil || p.Opcode != 0x0504 || p.ID != model[i].allocation {
										t.Fatalf("seed %d trace %v: relay data ID=%x want=%x err=%v", seed, trace, p.ID, model[i].allocation, err)
									}
									delivered = true
								}
							}
							if !delivered {
								t.Fatalf("seed %d trace %v: selected relay did not send data", seed, trace)
							}
						}
					}
					if !found {
						t.Fatalf("seed %d trace %v: relay Ready lost endpoint/allocation ownership", seed, trace)
					}
				}
				selected = a.Link
			}
			if a.Kind == Send && failedPeers[a.Link.Address] {
				t.Fatalf("seed %d trace %v: send on retired candidate", seed, trace)
			}
		}
		if attemptFailed {
			if m.state != Closed {
				t.Fatalf("seed %d trace %v: attempt exit state=%v want Closed", seed, trace, m.state)
			}
			return
		}
		if selected.Address.IsValid() {
			want := Established
			if selectedFailed {
				want = Closed
			}
			if m.state != want {
				t.Fatalf("seed %d trace %v: selected path state=%v want=%v", seed, trace, m.state, want)
			}
			return
		}
		usable, ready := false, false
		for i, v := range model {
			if v.live && (v.direct || v.relay) && m.find(links[i]) < 0 {
				t.Fatalf("seed %d trace %v: server %d lost owned work", seed, trace, i)
			}
			usable = usable || v.live && (v.direct || v.relay)
			ready = ready || v.live && v.ready
		}
		for _, live := range livePeers {
			usable = usable || live
		}
		if usable && m.state == Closed {
			t.Fatalf("seed %d trace %v: attempt closed with owned alternatives", seed, trace)
		}
		if input.kind == "tick" && ready && m.state != Established {
			t.Fatalf("seed %d trace %v: ready relay stranded after selector tick", seed, trace)
		}
	}
	step := func(e Event, input pathInput) []Action {
		e.Attempt = 1
		out := m.Step(e, schedule.Now())
		check(out, input)
		return out
	}
	authenticated := func(i int, raw []byte, input pathInput, admitted func()) {
		wantRecovery := input.kind == "punch" && !attemptFailed && !selectedFailed && selected.Address.IsValid() && livePeers[selected.Address] && model[i].live
		opened := false
		header, _ := RecordHeader(4, config().RecordSID, 4+len(raw)+16)
		wire := append(append(header, 0x55, 0, 0, 0), make([]byte, len(raw)+16)...)
		out := m.Step(Event{Kind: Wire, Attempt: 1, Link: links[i], Data: wire}, schedule.Now())
		check(out, input)
		for _, a := range out {
			if a.Kind == Open {
				opened = true
				if input.kind == "holdOpen" {
					a.Data = append([]byte(nil), raw...)
					held = append(held, a)
				} else {
					if admitted != nil {
						admitted()
					}
					out := step(Event{Kind: Decrypted, Token: a.Token, Data: raw}, input)
					if input.kind == "punch" && !attemptFailed && !selectedFailed && selected.Address.IsValid() && livePeers[selected.Address] && model[i].live {
						recovered := false
						for _, send := range out {
							if send.Kind == Send && send.Link == selected {
								p, err := DecodeLegacy(send.Data, ScrambleKey(config().License.Mask))
								recovered = recovered || err == nil && p.Opcode == 0x0403
							}
						}
						if !recovered {
							t.Fatalf("seed %d trace %v: retained server lost post-Ready punch recovery", seed, trace)
						}
					}
				}
			}
		}
		if wantRecovery && !opened {
			t.Fatalf("seed %d trace %v: recovery ingress no longer owned", seed, trace)
		}
	}
	err := schedule.Run(core.Instant(time.Second), 1024, func(input pathInput, _ core.Instant) error {
		trace = append(trace, input)
		i := input.server
		v := &model[i]
		switch input.kind {
		case "refuse", "accept":
			raw := append([]byte(nil), h.fixtures[0x020d]...)
			put32(raw[36:], 0)
			if input.kind == "refuse" {
				put32(raw[36:], 0xfffffffb)
			}
			authenticated(i, raw, input, func() {
				if !selected.Address.IsValid() && v.live && v.relay && !v.accepted {
					if input.kind == "refuse" {
						v.relay = false
					} else if transport == UDP || relayOwner < 0 || relayOwner == i {
						v.accepted = true
						if relayOwner < 0 {
							relayOwner = i
						}
					}
				}
			})
		case "info":
			raw := append([]byte(nil), h.fixtures[0x030e]...)
			put32(raw[36:], uint32(v.allocation))
			authenticated(i, raw, input, func() {
				if !selected.Address.IsValid() && v.live && v.relay && v.accepted {
					v.ready = true
				}
			})
		case "punch":
			if transport != UDP {
				break
			}
			raw, addresses := propertyPunch(h.fixtures[0x030d], i)
			authenticated(i, raw, input, func() {
				if m.state == Connecting && v.live && v.direct {
					for _, addr := range addresses {
						if addr.IsValid() && !failedPeers[addr] {
							if !livePeers[addr] {
								peerOrder = append(peerOrder, addr)
							}
							livePeers[addr] = true
						}
					}
				}
			})
		case "fail":
			v.live = false
			v.ready = false
			if relayOwner == i {
				relayOwner = -1
			}
			if selected == links[i] {
				selectedFailed = true
			}
			step(Event{Kind: LinkFailed, Link: links[i]}, input)
		case "failPeers":
			for _, addr := range peerOrder {
				if livePeers[addr] {
					livePeers[addr] = false
					failedPeers[addr] = true
					if selected == (Link{Address: addr}) {
						selectedFailed = true
					}
					step(Event{Kind: LinkFailed, Link: Link{Address: addr}}, input)
				}
			}
		case "peer":
			for _, addr := range peerOrder {
				p := knock(0x0405, config().UID, material().RID)
				p.Body[28] = 1
				raw, _ := EncodeLegacy(p, ScrambleKey(config().License.Mask))
				step(Event{Kind: Wire, Link: Link{Address: addr}, Data: raw}, input)
			}
		case "complete", "rejectOpen", "failEffect":
			if len(held) == 0 {
				break
			}
			n := rng.IntN(len(held))
			pending := []Action{held[n]}
			held = append(held[:n], held[n+1:]...)
			if seed <= 3 || seed == 7 {
				pending = append(pending, held...)
				held = nil
			}
			for _, a := range pending {
				kind, data := Encrypted, make([]byte, len(a.Data)+16)
				if a.Kind == Open {
					kind, data = Decrypted, a.Data
				}
				if input.kind == "rejectOpen" {
					if a.Kind != Open {
						held = append(held, a)
						continue
					}
					kind = CryptoRejected
				}
				if input.kind == "failEffect" {
					kind = EffectFailed
					// The edge can fail live operations attempt-wide. Retired operations
					// must be harmless; ownership is modeled from public path outcomes.
					owned := false
					for j, l := range links {
						if a.Link == l && model[j].live {
							owned = a.Kind == Open || model[j].relay || (a.Kind == Seal && le16(a.Data[8:]) == 0x0240 && model[j].direct)
						}
					}
					if selected.Address.IsValid() {
						owned = a.Kind == Seal && a.Link == selected && le16(a.Data[8:]) == 0x030f
					}
					if owned && m.state != Closed {
						attemptFailed = true
					}
				}
				out := step(Event{Kind: kind, Token: a.Token, Link: a.Link, Data: data}, input)
				if kind == Encrypted && a.Kind == Seal && le16(a.Data[8:]) == 0x030f && a.Link == selected && !attemptFailed && !selectedFailed {
					delivered := false
					for _, send := range out {
						delivered = delivered || send.Kind == Send && send.Link == selected
					}
					if !delivered {
						t.Fatalf("seed %d trace %v: selected relay ACK was not sent", seed, trace)
					}
				}
			}
		case "holdOpen":
			if selected.Address.IsValid() {
				break
			}
			raw := append([]byte(nil), h.fixtures[0x020d]...)
			put16(raw[8:], 0xeeee) // authenticated but unknown: no model path transition
			authenticated(i, raw, input, nil)
		case "cancel":
			attemptFailed = true
			step(Event{Kind: Cancel}, input)
		case "failPeer", "peerHello", "peerACK":
			if len(peerOrder) == 0 {
				break
			}
			addr := peerOrder[rng.IntN(len(peerOrder))]
			if seed <= 6 {
				addr = peerOrder[i%len(peerOrder)]
			}
			if input.kind == "failPeer" {
				livePeers[addr] = false
				failedPeers[addr] = true
				if selected == (Link{Address: addr}) {
					selectedFailed = true
				}
				step(Event{Kind: LinkFailed, Link: Link{Address: addr}}, input)
			} else {
				op := uint16(0x0401)
				if input.kind == "peerACK" {
					op = 0x0404
				}
				if input.kind == "peerHello" && livePeers[addr] {
					handshakes[addr] = true
				}
				raw, _ := EncodeLegacy(knock(op, config().UID, material().RID), ScrambleKey(config().License.Mask))
				expectReady := !attemptFailed && m.state == Connecting && op == 0x0404 && livePeers[addr] && handshakes[addr]
				step(Event{Kind: Wire, Link: Link{Address: addr}, Data: raw}, input)
				if expectReady && !selected.Address.IsValid() {
					t.Fatalf("seed %d trace %v: owned handshake lost progress for %v", seed, trace, addr)
				}
			}
		case "tick":
			step(Event{Kind: Tick}, input)
		}
		check(nil, input)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Literal sockaddr slots from the 030d layout: one WAN and one LAN per server.
// The oracle knows these addresses without invoking the production parser.
func propertyPunch(fixture []byte, server int) ([]byte, []netip.AddrPort) {
	raw := append([]byte(nil), fixture...)
	clear(raw[16+24 : 16+104])
	clear(raw[16+120 : 16+248])
	var addresses []netip.AddrPort
	for n, offset := range []int{16 + 24, 16 + 40} {
		host := byte(2*server + n + 1)
		copy(raw[offset:offset+8], []byte{2, 0, 0x9c, 0x40, 198, 51, 100, host})
		addresses = append(addresses, netip.AddrPortFrom(netip.AddrFrom4([4]byte{198, 51, 100, host}), 40000))
	}
	return raw, addresses
}
