package iotc

import (
	"bytes"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"github.com/go-json-experiment/json"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// This synchronous edge drives only public events. Target replies use literal
// inner frames and stdlib GCM, never Packet.Marshal, RecordHeader or EncodeLegacy.
// Only the prerequisite hello uses the existing encoder; it is not under test.
type matrixPeer struct {
	observe         func(Event, []Action)
	holdDerive      bool
	derivations     []Action
	t               *testing.T
	h               *harness
	now             core.Instant
	transport       Transport
	serverSID       uint32
	server, master  Link
	sent            map[uint16]int
	ready, rejected int
}

func newMatrixPeer(t *testing.T, attempt uint64, identity string, transport Transport, holdDerive ...bool) *matrixPeer {
	return newMatrixPeerObserved(t, attempt, identity, transport, nil, holdDerive...)
}

func newMatrixPeerObserved(t *testing.T, attempt uint64, identity string, transport Transport, observe func(Event, []Action), holdDerive ...bool) *matrixPeer {
	t.Helper()
	h := newHarness(t, "silent", 0)
	cfg := config()
	cfg.Attempt = attempt
	mat := material()
	mat.HelloSerial += uint32(attempt)
	mat.MasterKey[0] += byte(attempt)
	mat.MasterIV[0] += byte(attempt)
	h.clientPrivate = testPrivate(t, byte(attempt+2))
	der, err := x509.MarshalPKIXPublicKey(h.clientPrivate.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	copy(mat.Public[:], der)
	sid := uint32(0x55)
	if attempt == 2 {
		switch identity {
		case "same-fields": // Deliberate alias control, beyond fresh-RID policy.
		case "same-short-ids":
			mat.RID[7]++
		case "different-fields":
			cfg.RecordSID = 8
			mat.P2PNonce++
			mat.MasterNonce++
			mat.RID = [8]byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0, 0x11}
			sid++
		default:
			t.Fatal(identity)
		}
	}
	h.mat = mat
	h.install(1, mat.MasterKey, mat.MasterIV)
	h.m, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := &matrixPeer{observe: observe, t: t, h: h, transport: transport, serverSID: sid, sent: map[uint16]int{}}
	if len(holdDerive) > 0 {
		p.holdDerive = holdDerive[0]
	}
	p.run(p.step(Event{Kind: Start}))
	for _, v := range h.m.endpoints[:h.m.count] {
		if v.master && v.link.Transport == transport && v.connected {
			p.master = v.link
			break
		}
	}
	if !p.master.Address.IsValid() {
		t.Fatal("missing master")
	}
	p.deliver(p.master, p.record(0x100c, nil))
	for _, v := range h.m.endpoints[:h.m.count] {
		if !v.master && v.link.Transport == transport && v.connected {
			p.server = v.link
			break
		}
	}
	if !p.server.Address.IsValid() {
		t.Fatal("missing server")
	}
	if transport == UDP {
		// Echo the actual issued hello. No stage/correlation fields are installed.
		v := h.m.endpoints[h.m.find(p.server)]
		raw := unhex("04021d001800000004803f000000000002009c40c000020a00000000000000000000000000000000")
		binary.LittleEndian.PutUint32(raw[32:], v.hello)
		binary.LittleEndian.PutUint16(raw[36:], uint16(cfg.RecordSID))
		q, err := ParsePacket(raw)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := EncodeLegacy(q, h.m.key)
		if err != nil {
			t.Fatal(err)
		}
		p.deliver(p.server, wire)
	}
	if !p.holdDerive && p.sent[0x0217] == 0 {
		t.Fatal("precheck not issued")
	}
	return p
}

func (p *matrixPeer) step(e Event) []Action {
	e.Attempt = p.h.m.cfg.Attempt // CURRENT socket owner, including old wire bytes.
	actions := p.h.m.Step(e, p.now)
	if p.observe != nil {
		p.observe(e, actions)
	}
	return actions
}

func (p *matrixPeer) run(queue []Action) {
	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		var e Event
		switch a.Kind {
		case Generate:
			e = Event{Kind: MaterialReady, Token: a.Token, Material: p.h.mat}
		case Resolve:
			e = Event{Kind: Resolved, Token: a.Token, Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.100")}}
		case OpenTCP:
			e = Event{Kind: LinkFailed, Link: a.Link}
			if p.transport == TCP && a.Link.Address.Port() == 443 {
				e.Kind = Connected
			}
		case Derive:
			if p.holdDerive {
				p.derivations = append(p.derivations, a)
				continue
			}
			public, err := parseTestPublic(a.Public[:])
			if err != nil {
				p.t.Fatal(err)
			}
			pair, err := testPair(p.h.clientPrivate, public, [12]byte{byte(p.h.m.cfg.Attempt), 8})
			if err != nil {
				p.t.Fatal(err)
			}
			p.h.keys[3], p.h.ivs[3] = pair.aead, pair.iv
			e = Event{Kind: Derived, Token: a.Token, Key: 3, IV: pair.iv}
		case Seal:
			b := make([]byte, 256)
			if a.Mode != RawMasterRSA {
				iv := p.h.ivs[a.Key]
				b = p.h.keys[a.Key].Seal(nil, iv[:], a.Data, a.AAD)
			}
			e = Event{Kind: Encrypted, Token: a.Token, Data: b}
		case Open:
			e = p.open(a)
		case Send:
			var raw []byte
			if len(a.Data) >= 12 && a.Data[0] == 0x51 && a.Data[1] == 0xcc {
				prefix, key := 16, KeyRef(3)
				if a.Data[5] == 1 {
					p.sent[0x100b]++
					continue
				}
				if a.Data[5] == 2 {
					prefix = 116
				}
				iv := p.h.ivs[key]
				var err error
				raw, err = p.h.keys[key].Open(nil, iv[:], a.Data[prefix:], a.Data[:prefix])
				if err != nil {
					p.t.Fatal(err)
				}
			} else {
				q, err := DecodeLegacy(a.Data, p.h.m.key)
				if err != nil {
					p.t.Fatal(err)
				}
				p.sent[q.Opcode]++
				continue
			}
			p.sent[binary.LittleEndian.Uint16(raw[8:])]++
			continue
		case Ready:
			p.ready++
			continue
		case Failed, Disposed:
			p.t.Fatalf("unexpected terminal action %v: %s", a.Kind, a.Reason)
		default:
			continue
		}
		queue = append(queue, p.step(e)...)
	}
}

func (p *matrixPeer) open(a Action) Event {
	iv := p.h.ivs[a.Key]
	raw, err := p.h.keys[a.Key].Open(nil, iv[:], a.Data, a.AAD)
	if err != nil {
		p.rejected++
		return Event{Kind: CryptoRejected, Token: a.Token}
	}
	return Event{Kind: Decrypted, Token: a.Token, Data: raw}
}
func (p *matrixPeer) deliver(l Link, wire []byte) {
	p.run(p.step(Event{Kind: Wire, Link: l, Data: wire, Marker: Marker{100, 123}}))
}

// Literal offsets are measured from H. Only the listed identity fields vary;
// receive-only opaque bytes and all lengths stay independent of the encoder.
func (p *matrixPeer) plain(op uint16) []byte {
	var b []byte
	if op == 0x0218 {
		b = unhex("04021d0038000000180224000000000034120000550000004558414d504c455549443030303030303030303000000000000000000000000000000000030000000008030400020000")
	} else {
		b = bytes.Clone(p.h.fixtures[op])
	}
	if len(b) < 16 {
		p.t.Fatalf("missing literal %04x", op)
	}
	switch op {
	case 0x100c:
		binary.LittleEndian.PutUint16(b[16:], p.h.mat.MasterNonce)
	case 0x0218:
		binary.LittleEndian.PutUint16(b[16:], p.h.mat.P2PNonce)
		binary.LittleEndian.PutUint32(b[20:], p.serverSID)
	case 0x030d:
		binary.LittleEndian.PutUint16(b[16:], p.h.mat.P2PNonce)
		copy(b[120:], p.h.mat.RID[:])
	case 0x020d:
		binary.LittleEndian.PutUint32(b[36:], 0)
		copy(b[40:], p.h.mat.RID[:])
	case 0x030e:
		copy(b[72:], p.h.mat.RID[:])
	}
	return b
}

func (p *matrixPeer) record(op uint16, plain []byte) []byte {
	if plain == nil {
		plain = p.plain(op)
	}
	typ, prefix := byte(4), 16
	key := KeyRef(3)
	if op == 0x100c {
		typ, prefix, key = 1, 12, 1
	}
	if op == 0x0218 {
		typ, prefix = 3, 12
	}
	aad := make([]byte, prefix)
	copy(aad, unhex("51cc00000200000007000000"))
	aad[5] = typ
	binary.LittleEndian.PutUint16(aad[6:], uint16(prefix-12+len(plain)+16))
	binary.LittleEndian.PutUint32(aad[8:], p.h.m.cfg.RecordSID)
	if prefix == 16 {
		binary.LittleEndian.PutUint32(aad[12:], p.serverSID)
	}
	pair := peerPair{p.h.keys[key], p.h.ivs[key]}
	// The server independently derives from the client's published public slot.
	if key == 3 {
		public, err := parseTestPublic(p.h.mat.Public[:])
		if err != nil {
			p.t.Fatal(err)
		}
		var err2 error
		pair, err2 = testPair(p.h.serverPrivate, public, pair.iv)
		if err2 != nil {
			p.t.Fatal(err2)
		}
	}
	return append(aad, pair.aead.Seal(nil, pair.iv[:], plain, aad)...)
}

func matrixKnock(t *testing.T, op uint16, identity string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/attempt-knocks.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct{ Identity, Opcode, Plain, Wire string }
	if err = json.Unmarshal(b, &rows, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Identity == identity && r.Opcode == fmt.Sprintf("%04x", op) {
			wire := unhex(r.Wire)
			q, err := DecodeLegacy(wire, ScrambleKey([16]byte{}))
			if err != nil {
				t.Fatal(err)
			}
			// Verify the independently scrambled fixture decodes to the literal frame.
			raw, err := q.Marshal()
			if err != nil || !bytes.Equal(raw, unhex(r.Plain)) {
				t.Fatal("knock fixture mismatch")
			}
			return wire
		}
	}
	t.Fatal("missing knock literal")
	return nil
}

func (p *matrixPeer) rendezvous() {
	p.deliver(p.server, p.record(0x0218, nil))
	if p.transport == TCP {
		p.now = core.Instant(1500 * time.Millisecond)
		p.run(p.step(Event{Kind: Tick}))
	}
	if p.sent[0x020c] == 0 {
		p.t.Fatal("rendezvous not started")
	}
}
func (p *matrixPeer) prepare(op uint16) {
	if op == 0x0218 {
		return
	}
	p.rendezvous()
	if op == 0x030e {
		p.deliver(p.server, p.record(0x020d, nil))
		if p.sent[0x0241] == 0 {
			p.t.Fatal("info not requested")
		}
	}
	if op >= 0x0401 && op <= 0x0406 {
		p.deliver(p.server, p.record(0x030d, nil))
		if p.sent[0x0403] == 0 {
			p.t.Fatal("no knock candidates")
		}
		if op == 0x0404 || op == 0x0406 {
			p.deliver(p.peerLink(), matrixKnock(p.t, 0x0401, p.knockIdentity()))
		}
	}
}
func (p *matrixPeer) peerLink() Link {
	return Link{Address: netip.MustParseAddrPort("198.51.100.20:41000")}
}
func (p *matrixPeer) knockIdentity() string {
	if p.h.mat.RID == material().RID {
		return "old"
	}
	if p.h.mat.RID[0] == 0x44 {
		return "alias"
	}
	return "different"
}

func TestAttemptMatrixLiteralPeers(t *testing.T) {
	for _, tr := range []Transport{UDP, TCP} {
		t.Run(fmt.Sprint(tr), func(t *testing.T) {
			p := newMatrixPeer(t, 1, "same-short-ids", tr)
			p.prepare(0x030e)
			p.deliver(p.server, p.record(0x030e, nil))
			// Relay selection occurs on the next timer after info and ACK.
			p.now += core.Instant(10 * time.Millisecond)
			p.run(p.step(Event{Kind: Tick}))
			if p.ready != 1 || p.h.m.State() != Established {
				t.Fatalf("ready=%d state=%v", p.ready, p.h.m.State())
			}
		})
	}
	for _, op := range []uint16{0x0402, 0x0405, 0x0404, 0x0406} {
		t.Run(fmt.Sprintf("%04x", op), func(t *testing.T) {
			p := newMatrixPeer(t, 1, "same-short-ids", UDP)
			p.prepare(op)
			p.deliver(p.peerLink(), matrixKnock(t, op, "old"))
			if p.ready != 1 || !p.h.m.direct {
				t.Fatalf("ready=%d direct=%v", p.ready, p.h.m.direct)
			}
		})
	}
}
