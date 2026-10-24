package iotc

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/x509"
	"encoding/json/v2"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"net/netip"
	"os"
	"testing"
	"time"
)

type harness struct {
	allowTCP                     bool
	udpMasterAt, udpPrecheckAt   core.Instant
	mat                          Material
	clientPrivate, serverPrivate *ecdh.PrivateKey
	peerPairs                    map[netip.Addr]peerPair
	requests                     map[uint16][]byte
	t                            *testing.T
	m                            *Machine
	net                          *sim.Network
	timer                        core.Instant
	mode                         string
	impair                       sim.Link
	links                        map[string]Link
	keys                         map[KeyRef]cipher.AEAD
	ivs                          map[KeyRef][12]byte
	fixtures                     map[uint16][]byte
	ready, received, disposed    int
	trace                        []string
	held                         []Action
	hold                         bool
	holdRelayACK                 bool
	heldSeals                    []Action
	relayACK                     map[Link]bool
	dropACK                      bool
	progress                     []RelayProgress
	readyProgress                RelayProgress
	failures                     []Action
}

func newHarness(t *testing.T, mode string, loss int) *harness {
	m, e := New(config())
	if e != nil {
		t.Fatal(e)
	}
	h := &harness{t: t, m: m, mode: mode, net: sim.NewNetwork(42, 1<<20, 2048), impair: sim.Link{MTU: 1416, LossPPM: loss, DuplicatePPM: 100000, Delay: time.Millisecond, Jitter: 5 * time.Millisecond}, links: map[string]Link{}, keys: map[KeyRef]cipher.AEAD{}, ivs: map[KeyRef][12]byte{}, fixtures: map[uint16][]byte{}}
	b, e := os.ReadFile("testdata/modern-fixtures.json")
	if e != nil {
		t.Fatal(e)
	}
	var fs []struct{ Hex string }
	json.Unmarshal(b, &fs, json.MatchCaseInsensitiveNames(true))
	for _, f := range fs {
		b := unhex(f.Hex)
		h.fixtures[le16(b[8:])] = b
	}
	h.install(1, material().MasterKey, material().MasterIV)
	h.clientPrivate = testPrivate(t, 2)
	h.serverPrivate = testPrivate(t, 1)
	h.mat = material()
	der, err := x509.MarshalPKIXPublicKey(h.clientPrivate.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	copy(h.mat.Public[:], der)
	h.peerPairs = map[netip.Addr]peerPair{}
	h.requests = map[uint16][]byte{}
	requestBytes, err := os.ReadFile("testdata/wire-literals.json")
	if err != nil {
		t.Fatal(err)
	}
	var requests []struct{ Hex string }
	if err = json.Unmarshal(requestBytes, &requests, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	for _, f := range requests {
		raw := unhex(f.Hex)
		h.requests[le16(raw[8:])] = raw
	}
	return h
}
func (h *harness) install(ref KeyRef, key [16]byte, iv [12]byte) {
	b, e := aes.NewCipher(key[:])
	if e != nil {
		h.t.Fatal(e)
	}
	a, e := cipher.NewGCM(b)
	if e != nil {
		h.t.Fatal(e)
	}
	h.keys[ref] = a
	h.ivs[ref] = iv
}
func linkName(l Link) string { return fmt.Sprintf("%d/%s/%d", l.Transport, l.Address, l.ID) }
func (h *harness) step(e Event) {
	e.Attempt = h.m.cfg.Attempt
	now := h.net.Scheduler.Now()
	actions := h.m.Step(e, now)
	h.actions(actions)
}
func (h *harness) seal(a Action) {
	var b []byte
	if a.Mode == RawMasterRSA {
		b = append([]byte(nil), a.Data...)
	} else {
		iv := h.ivs[a.Key]
		b = h.keys[a.Key].Seal(nil, iv[:], a.Data, a.AAD)
	}
	h.step(Event{Kind: Encrypted, Token: a.Token, Data: b})
}

func (h *harness) actions(actions []Action) {
	for _, a := range actions {
		switch a.Kind {
		case Generate:
			h.step(Event{Kind: MaterialReady, Token: a.Token, Material: h.mat})
		case Resolve:
			h.step(Event{Kind: Resolved, Token: a.Token, Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.100")}})
		case OpenTCP:
			h.links[linkName(a.Link)] = a.Link
			if h.mode != "tcp" && !h.allowTCP {
				h.step(Event{Kind: LinkFailed, Link: a.Link})
			} else if a.Link.Address.Port() != 443 {
				h.step(Event{Kind: LinkFailed, Link: a.Link})
			} else {
				h.step(Event{Kind: Connected, Link: a.Link})
			}
		case Derive:
			public, err := parseTestPublic(a.Public[:])
			if err != nil {
				h.t.Fatal(err)
			}
			pair, err := testPair(h.clientPrivate, public, [12]byte{8})
			if err != nil {
				h.t.Fatal(err)
			}
			h.keys[3] = pair.aead
			h.ivs[3] = pair.iv
			h.step(Event{Kind: Derived, Token: a.Token, Key: 3, IV: pair.iv})
		case Seal:
			if h.holdRelayACK && len(a.Data) >= 16 && le16(a.Data[8:]) == 0x030f {
				h.heldSeals = append(h.heldSeals, a)
			} else {
				h.seal(a)
			}
		case Open:
			if h.hold {
				h.held = append(h.held, a)
				continue
			}
			iv := h.ivs[a.Key]
			b, e := h.keys[a.Key].Open(nil, iv[:], a.Data, a.AAD)
			if e != nil {
				h.t.Fatal(e)
			}
			h.step(Event{Kind: Decrypted, Token: a.Token, Data: b})
		case Send:
			h.links[linkName(a.Link)] = a.Link
			h.send(sim.Packet{From: "client", To: linkName(a.Link), Data: a.Data})
		case ArmTimer:
			h.timer = a.At
		case CancelTimer:
			h.timer = 0
		case Ready:
			h.readyProgress = a.Progress
			h.ready++
			h.trace = append(h.trace, "ready")
		case Received:
			h.received++
			h.trace = append(h.trace, fmt.Sprintf("data:%x", a.Data))
		case Disposed:
			h.disposed++
		case RelayProgressChanged:
			h.progress = append(h.progress, a.Progress)
		case Failed:
			h.failures = append(h.failures, a)
			h.trace = append(h.trace, a.Reason)
		}
	}
}
func (h *harness) send(p sim.Packet) {
	l := h.impair
	if h.mode == "tcp" {
		link := h.links[p.To]
		if p.To == "client" {
			link = h.links[p.From]
		}
		if link.Transport == UDP {
			return
		}
		l.LossPPM = 0
		l.DuplicatePPM = 0
		l.Jitter = 0
	}
	// Hold independently progressing UDP replies until the requested absolute
	// times. TCP continues through discovery/precheck while UDP is delayed.
	if h.allowTCP && p.To == "client" {
		link := h.links[p.From]
		if link.Transport == UDP && len(p.Data) >= 12 && le32(p.Data) == 0xcc51 {
			var at core.Instant
			switch p.Data[5] {
			case 1:
				at = h.udpMasterAt
			case 3:
				at = h.udpPrecheckAt
			}
			if at > h.net.Scheduler.Now() {
				l.Delay = max(l.Delay, at.Sub(h.net.Scheduler.Now()))
			}
		}
	}
	if e := h.net.Send(p, l); e != nil {
		h.t.Fatal(e)
	}
}
func (h *harness) reply(l Link, b []byte) {
	h.send(sim.Packet{From: linkName(l), To: "client", Data: b})
}
func (h *harness) encrypted(l Link, typ byte, plain []byte) {
	prefix := 0
	key := KeyRef(3)
	if typ == 1 {
		key = 1
	}
	if typ == 4 {
		prefix = 4
	}
	aad, _ := RecordHeader(typ, 7, prefix+len(plain)+16)
	if typ == 4 {
		aad = append(aad, 0x55, 0, 0, 0)
	}
	pair := h.peerPairs[l.Address.Addr()]
	if typ == 1 {
		pair = peerPair{h.keys[key], h.ivs[key]}
	}
	if pair.aead == nil {
		h.t.Fatal("reply without published key agreement")
	}
	b := pair.aead.Seal(nil, pair.iv[:], plain, aad)
	h.reply(l, append(aad, b...))
}
func (h *harness) legacy(l Link, p Packet) {
	b, e := EncodeLegacy(p, h.m.key)
	if e != nil {
		h.t.Fatal(e)
	}
	h.reply(l, b)
}
func (h *harness) server(l Link, b []byte) {
	if len(b) >= 4 && le32(b) == 0xcc51 {
		plain, e := h.requestPlain(l, b)
		if e != nil {
			// The selected-relay fixture server stays silent on nonconforming 0241.
			if len(plain) >= 16 && le16(plain[8:]) == 0x0241 {
				return
			}
			h.t.Fatal(e)
		}
		p, e := ParsePacket(plain)
		if e != nil {
			h.t.Fatal(e)
		}
		h.trace = append(h.trace, fmt.Sprintf("%04x", p.Opcode))
		switch p.Opcode {
		case 0x100b:
			h.encrypted(l, 1, h.fixtures[0x100c])
		case 0x0217:
			q := controlPacket(0x0218, 0x24, 56)
			put16(q.Body, h.mat.P2PNonce)
			put32(q.Body[4:], 0x55)
			uid := config().UID
			copy(q.Body[8:], uid[:])
			put32(q.Body[44:], 3)
			raw, _ := q.Marshal()
			h.encrypted(l, 3, raw)
		case 0x0240:
			if h.mode == "direct" || h.mode == "punched" || h.mode == "silent" {
				if h.mode != "silent" {
					h.encrypted(l, 4, h.fixtures[0x030d])
				}
			}
		case 0x020c:
			if h.mode == "relay" || h.mode == "tcp" {
				raw := append([]byte(nil), h.fixtures[0x020d]...)
				put32(raw[36:], 0)
				h.encrypted(l, 4, raw)
			}
		case 0x0241:
			h.encrypted(l, 4, h.fixtures[0x030e])
		case 0x030f:
			if h.relayACK == nil {
				h.relayACK = map[Link]bool{}
			}
			h.relayACK[l] = true
		}
		return
	}
	p, e := DecodeLegacy(b, h.m.key)
	if e != nil {
		return
	}
	h.trace = append(h.trace, fmt.Sprintf("%04x", p.Opcode))
	switch p.Opcode {
	case 0x8003:
		q := controlPacket(0x8004, 0x3f, 24)
		copy(q.Body, addressBytes(netip.MustParseAddrPort("192.0.2.10:40000")))
		copy(q.Body[16:], p.Body[:6])
		h.legacy(l, q)
	case 0x0403:
		if h.mode == "punched" && l.Address.String() != "203.0.113.20:41001" {
			return
		}
		q := knock(0x0405, config().UID, h.mat.RID)
		q.Body[28] = 1
		h.legacy(l, q)
	case 0x0404:
		if h.dropACK {
			h.dropACK = false
			q := knock(0x0405, config().UID, h.mat.RID)
			q.Body[28] = 1
			h.legacy(l, q)
		}
	case 0x0407, 0x0504:
		if p.Opcode == 0x0504 && !h.relayACK[l] {
			return
		}
		if p.Opcode == 0x0407 {
			p.Opcode = 0x0408
			p.Route = 0x12
		} else {
			p.Opcode = 0x0503
		}
		h.legacy(l, p)
	case 0x0427, 0x0524:
		if p.Opcode == 0x0427 {
			p.Opcode = 0x0428
			p.Route = 0x12
		} else {
			p.Opcode = 0x0523
		}
		h.legacy(l, p)
	}
}
func (h *harness) run(d time.Duration) {
	end := h.net.Scheduler.Now().Add(d)
	for h.net.Scheduler.Now() < end {
		until := min(end, h.net.Scheduler.Now().Add(time.Millisecond))
		if e := h.net.Scheduler.Run(until, 10000, func(p sim.Packet, at core.Instant) error {
			if p.To == "client" {
				h.step(Event{Kind: Wire, Link: h.links[p.From], Data: p.Data, Marker: Marker{100, 123}})
			} else {
				h.server(h.links[p.To], p.Data)
			}
			return nil
		}); e != nil {
			h.t.Fatal(e)
		}
		if h.timer != 0 && h.net.Scheduler.Now() >= h.timer {
			h.step(Event{Kind: Tick})
		}
	}
}
func TestSimulatorNATOutcomes(t *testing.T) {
	for _, mode := range []string{"direct", "punched", "relay", "tcp"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, mode, 200000)
			h.dropACK = true
			h.step(Event{Kind: Start})
			h.run(10 * time.Second)
			if h.ready != 1 {
				t.Fatalf("ready %d trace %v", h.ready, h.trace)
			}
			if (mode == "tcp") != (h.m.path.Transport == TCP) {
				t.Fatal("wrong transport")
			}
			if (mode == "direct" || mode == "punched") != h.m.direct {
				t.Fatal("wrong path")
			}
			for i := 0; i < 5; i++ {
				h.step(Event{Kind: Write, Data: []byte{0x17, 0xfe, 0xfd, 1, 2}})
			}
			h.run(3 * time.Second)
			if h.received == 0 {
				t.Fatal("no packet delivery")
			}
			h.step(Event{Kind: Cancel})
			h.run(time.Second)
			if h.disposed != 1 || h.m.state != Closed {
				t.Fatal("cleanup")
			}
		})
	}
}
func TestSimulatorSilentAndStale(t *testing.T) {
	h := newHarness(t, "silent", 0)
	h.step(Event{Kind: Start})
	h.run(time.Second)
	h.hold = true
	h.run(time.Second)
	h.step(Event{Kind: Cancel})
	for _, a := range h.held {
		iv := h.ivs[a.Key]
		b, _ := h.keys[a.Key].Open(nil, iv[:], a.Data, a.AAD)
		h.step(Event{Kind: Decrypted, Token: a.Token, Data: b})
	}
	if h.ready != 0 || h.disposed != 1 {
		t.Fatal("stale completion resurrected")
	}
	h = newHarness(t, "silent", 1000000)
	h.step(Event{Kind: Start})
	h.run(61 * time.Second)
	if h.disposed != 1 || h.m.state != Closed {
		t.Fatal("loss timeout")
	}
}

func TestAcceleratedEightHours(t *testing.T) {
	m := established(t, true)
	s := sim.New[Event](4096, 8)
	timer := sim.NewTimer(s)
	dispatch := func(actions []Action) {
		for _, a := range actions {
			switch a.Kind {
			case ArmTimer:
				if e := timer.Arm(a.At, Event{Kind: Tick, Attempt: 1}); e != nil {
					t.Fatal(e)
				}
			case Send:
				p, e := DecodeLegacy(a.Data, m.key)
				if e != nil {
					t.Fatal(e)
				}
				if p.Opcode == 0x0427 {
					p.Opcode = 0x0428
					p.Route = 0x12
					b, _ := EncodeLegacy(p, m.key)
					if e := s.Schedule(s.Now().Add(time.Millisecond), Event{Kind: Wire, Attempt: 1, Link: m.path, Data: b}, len(b)); e != nil {
						t.Fatal(e)
					}
				}
			}
		}
	}
	dispatch([]Action{{Kind: ArmTimer, At: core.Instant(time.Second)}})
	if e := s.Run(core.Instant(8*time.Hour), 100000, func(e Event, at core.Instant) error { dispatch(m.Step(e, at)); return nil }); e != nil {
		t.Fatal(e)
	}
	if m.state != Established {
		t.Fatal("quiet session expired")
	}
	n := 0
	for _, p := range m.pending {
		if p.token != 0 {
			n++
		}
	}
	if n != 0 || m.out != nil || s.PeakBytes > 24 || s.PeakCount > 2 {
		t.Fatalf("retained growth pending=%d bytes=%d events=%d", n, s.PeakBytes, s.PeakCount)
	}
}

func TestSimulatorMTUAndSlowConsumer(t *testing.T) {
	t.Run("mtu timeout", func(t *testing.T) {
		h := newHarness(t, "direct", 0)
		h.impair.MTU = 64
		h.step(Event{Kind: Start})
		h.run(61 * time.Second)
		if h.ready != 0 || h.disposed != 1 || h.net.Dropped == 0 {
			t.Fatal("MTU loss did not terminate within budget")
		}
	})
	t.Run("slow receive and wall jumps", func(t *testing.T) {
		h := newHarness(t, "relay", 100000)
		h.impair.ConsumerDelay = 50 * time.Millisecond
		h.step(Event{Kind: Start})
		h.net.Scheduler.JumpWall(12 * time.Hour)
		h.run(5 * time.Second)
		h.net.Scheduler.JumpWall(-24 * time.Hour)
		h.run(5 * time.Second)
		if h.ready != 1 || h.m.state != Established {
			t.Fatal("slow consumer or wall time broke monotonic attempt")
		}
		if h.net.Scheduler.PeakBytes > 1<<20 || h.net.Scheduler.PeakCount > 2048 {
			t.Fatal("network budget")
		}
	})
}
func TestSimulatorDeterministicReplay(t *testing.T) {
	run := func() []string {
		h := newHarness(t, "punched", 200000)
		h.step(Event{Kind: Start})
		h.run(10 * time.Second)
		return h.trace
	}
	first, second := run(), run()
	if fmt.Sprint(first) != fmt.Sprint(second) {
		t.Fatal("seed replay diverged")
	}
}
