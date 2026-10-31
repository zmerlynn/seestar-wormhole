package iotc

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/edge"
	"net/netip"
	"reflect"
	"testing"
	"time"
)

// Drive discovery through public events. Authentication is supplied by a mock
// edge; no endpoint/stage/serial field is installed or changed by this helper.
func freshMaster(t *testing.T, attempt uint64, sid uint32, starts ...uint32) (*Machine, Link, Packet) {
	t.Helper()
	cfg := config()
	cfg.Attempt = attempt
	cfg.RecordSID = sid
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	step := func(e Event) []Action { e.Attempt = attempt; return m.Step(e, 0) }
	initial := step(Event{Kind: Start})
	mat := material()
	// Deterministic injected RNG at the edge; production uses crypto/rand.Reader.
	random := []byte{byte(attempt), 0x32, 0x54, byte(attempt + 0x70)}
	mat.HelloSerial, err = edge.HelloSerialStart(bytes.NewReader(random))
	if err != nil {
		t.Fatal(err)
	}
	if len(starts) != 0 {
		mat.HelloSerial = starts[0]
	}
	// Distinct, valid attempt material. The mock edge owns crypto correctness.
	mat.RID[0] = byte(attempt)
	mat.MasterNonce += uint16(attempt)
	mat.P2PNonce += uint16(attempt)
	mat.MasterKey[0] += byte(attempt)
	mat.MasterIV[0] += byte(attempt)
	der, err := x509.MarshalPKIXPublicKey(testPrivate(t, byte(attempt+2)).PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	copy(mat.Public[:], der)
	mat.MasterRef += KeyRef(attempt * 10)
	mat.PrivateRef += KeyRef(attempt * 10)
	for _, a := range initial {
		if a.Kind == Generate {
			step(Event{Kind: MaterialReady, Token: a.Token, Material: mat})
		}
	}
	var actions []Action
	for _, a := range initial {
		if a.Kind == Resolve {
			actions = step(Event{Kind: Resolved, Token: a.Token, Addresses: []netip.Addr{netip.MustParseAddr("192.0.2.100")}})
		}
	}
	for _, a := range actions {
		if a.Kind == Seal {
			step(Event{Kind: Encrypted, Token: a.Token, Data: make([]byte, 256)})
		}
	}
	l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	q := controlPacket(0x100c, 0x24, 60+4+108)
	put16(q.Body, mat.MasterNonce)
	copy(q.Body[4:], cfg.UID[:])
	put16(q.Body[58:], 1)
	q.Body[60] = 9
	put16(q.Body[62:], 108)
	copy(q.Body[64:], addressBytes(l.Address))
	serverDER, err := x509.MarshalPKIXPublicKey(testPrivate(t, 1).PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	copy(q.Body[80:], serverDER)
	return m, Link{Address: netip.AddrPortFrom(m.masters[0], 10240)}, q
}

func freshHello(t *testing.T, attempt uint64, sid uint32, starts ...uint32) (*Machine, Link, Packet) {
	t.Helper()
	m, master, q := freshMaster(t, attempt, sid, starts...)
	l, err := ParseAddress(q.Body[64:80])
	if err != nil {
		t.Fatal(err)
	}
	step := func(e Event) []Action { e.Attempt = attempt; return m.Step(e, 0) }
	raw, _ := q.Marshal()
	header, _ := RecordHeader(1, sid, len(raw)+16)
	actions := step(Event{Kind: Wire, Link: master, Data: append(header, make([]byte, len(raw)+16)...), Marker: Marker{10, 1}})
	for _, a := range actions {
		if a.Kind == Open {
			for _, b := range step(Event{Kind: Decrypted, Token: a.Token, Data: raw}) {
				if b.Kind == Send {
					p, e := DecodeLegacy(b.Data, m.key)
					if e == nil && p.Opcode == 0x8003 {
						return m, Link{Address: l}, p
					}
				}
			}
		}
	}
	t.Fatal("no hello from discovery")
	return nil, Link{}, Packet{}
}

func TestFreshPreviousAttemptHello(t *testing.T) {
	for _, sid := range []uint32{7, 8, 65543} {
		for _, alternate := range []bool{false, true} {
			for _, retried := range []bool{false, true} {
				for _, short := range []bool{false, true} {
					t.Run(fmt.Sprintf("SID=%d/alternate=%v/retried=%v/short=%v", sid, alternate, retried, short), func(t *testing.T) {
						old, l, hello := freshHello(t, 1, 7)
						q := controlPacket(0x8004, 0x3f, 24)
						copy(q.Body, addressBytes(netip.MustParseAddrPort("198.51.100.77:40000")))
						copy(q.Body[16:], hello.Body[:6])
						if alternate {
							q.Opcode = 0x8005
							l.Address = netip.AddrPortFrom(l.Address.Addr(), 10002)
						}
						if short {
							q.Body = q.Body[:16]
						}
						wire, _ := EncodeLegacy(q, old.key)
						old.Step(Event{Kind: Cancel, Attempt: 1}, 1)
						current, _, nowHello := freshHello(t, 2, sid)
						// A datagram arriving on the replacement socket is stamped with its owner,
						// not with an unknowable attempt ID from the remote sender.
						at := core.Instant(time.Millisecond)
						if retried {
							current.Step(Event{Kind: Tick, Attempt: 2}, core.Instant(500*time.Millisecond))
							at = core.Instant(501 * time.Millisecond)
						}
						out := current.Step(Event{Kind: Wire, Attempt: 2, Link: l, Data: wire}, at)
						if sid == 8 { // Different on-wire instance is a passing isolation control.
							if len(out) != 1 || out[0].Kind != ArmTimer {
								t.Fatal("different instance accepted")
							}
							return
						}
						for _, a := range out {
							if a.Kind == Derive || a.Kind == Seal {
								t.Fatalf("previous-attempt datagram advanced fresh discovery: old serial=%d current serial=%d action=%v NAT=%d; want ignored", le32(hello.Body), le32(nowHello.Body), a.Kind, current.nat.Type())
							}
						}
						if current.nat.count != 0 {
							t.Fatalf("old datagram recorded %d NAT observations; want 0", current.nat.count)
						}
					})
				}
			}
		}
	}
}

func TestHelloRandomOriginAndWrap(t *testing.T) {
	for _, start := range []uint32{1, 0x98badcfe, 0xfffffffe, 0xffffffff} {
		t.Run(fmt.Sprintf("%08x", start), func(t *testing.T) {
			m, l, first := freshHello(t, 1, 7, start)
			// Literal LE32 origin and LE16 instance, with no effect-token offset.
			want := []byte{byte(start), byte(start >> 8), byte(start >> 16), byte(start >> 24), 7, 0, 1, 0}
			if !bytes.Equal(first.Body, want) {
				t.Fatalf("first hello body=%x, want %x", first.Body, want)
			}
			serial := start
			for retry := 1; retry <= 3; retry++ {
				serial++
				if serial == 0 {
					serial = 1
				}
				found := false
				for _, a := range m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(time.Duration(retry)*500*time.Millisecond)) {
					if a.Kind != Send || a.Link != l {
						continue
					}
					q, err := DecodeLegacy(a.Data, m.key)
					if err == nil && q.Opcode == 0x8003 {
						found = true
						if le32(q.Body) != serial {
							t.Fatalf("retry serial=%x, want %x", le32(q.Body), serial)
						}
					}
				}
				if !found {
					t.Fatal("missing hello retry")
				}
			}
			q := controlPacket(0x8004, 0x3f, 24)
			copy(q.Body, addressBytes(netip.MustParseAddrPort("198.51.100.77:40000")))
			copy(q.Body[16:], first.Body[:6])
			wire, _ := EncodeLegacy(q, m.key)
			for _, a := range m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: wire}, core.Instant(1600*time.Millisecond)) {
				if a.Kind == Derive {
					return
				}
			}
			t.Fatal("original serial lost authority across wrap/retries")
		})
	}
}

// isolationPeer supplies authenticated plaintext at the mock edge boundary.
// All protocol setup and completions go through Step; no retained state is set.
type isolationPeer struct {
	t     *testing.T
	m     *Machine
	link  Link
	first Packet
	now   core.Instant
}

func (p *isolationPeer) step(e Event) []Action {
	e.Attempt = p.m.cfg.Attempt
	return p.m.Step(e, p.now)
}

func (p *isolationPeer) complete(out []Action) []Action {
	p.t.Helper()
	all := append([]Action(nil), out...)
	for len(out) != 0 {
		a := out[0]
		out = out[1:]
		var next []Action
		switch a.Kind {
		case Derive:
			next = p.step(Event{Kind: Derived, Token: a.Token, Key: 100 + KeyRef(p.m.cfg.Attempt), IV: [12]byte{1}})
		case Seal:
			next = p.step(Event{Kind: Encrypted, Token: a.Token, Data: make([]byte, len(a.Data)+16)})
		}
		all = append(all, next...)
		out = append(out, next...)
	}
	return all
}

func (p *isolationPeer) candidate() Link {
	return Link{Address: netip.AddrPortFrom(p.link.Address.Addr(), p.link.Address.Port()+1)}
}

func (p *isolationPeer) serverSID() uint32 { return 0x50 + uint32(p.m.cfg.Attempt) }

// typ zero denotes legacy framing; other values denote modern record types.
func (p *isolationPeer) reply(op uint16) (typ byte, l Link, raw []byte) {
	p.t.Helper()
	l = p.link
	var q Packet
	switch op {
	case 0x100c:
		typ, q = 1, p.first
	case 0x8004, 0x8005:
		q = controlPacket(op, 0x3f, 24)
		copy(q.Body, addressBytes(p.link.Address))
		copy(q.Body[16:], p.first.Body[:6])
	case 0x0218:
		typ, q = 3, controlPacket(op, 0x24, 56)
		put16(q.Body, p.m.material.P2PNonce)
		put32(q.Body[4:], p.serverSID())
		copy(q.Body[8:], p.m.cfg.UID[:])
	case 0x030d:
		typ, q = 4, controlPacket(op, 0x34, 296)
		put16(q.Body, p.m.material.P2PNonce)
		copy(q.Body[4:], p.m.cfg.UID[:])
		copy(q.Body[24:], addressBytes(p.candidate().Address))
		copy(q.Body[104:], p.m.material.RID[:])
	case 0x020d:
		typ, q = 4, controlPacket(op, 0x24, 32)
		copy(q.Body, p.m.cfg.UID[:])
		copy(q.Body[24:], p.m.material.RID[:])
	case 0x030e:
		typ, q = 4, controlPacket(op, 0x24, 88)
		copy(q.Body, p.m.cfg.UID[:])
		put32(q.Body[20:], 9)
		copy(q.Body[40:], addressBytes(p.candidate().Address))
		copy(q.Body[56:], p.m.material.RID[:])
	case 1, 2: // Type-14 offline/asleep alerts have no inner packet header.
		raw = make([]byte, 9)
		put32(raw, p.serverSID())
		put16(raw[4:], p.m.material.P2PNonce)
		raw[6] = byte(op)
		return 14, l, raw
	case 0x0401, 0x0402, 0x0404, 0x0405, 0x0406:
		l, q = p.candidate(), knock(op, p.m.cfg.UID, p.m.material.RID)
		if op == 0x0405 || op == 0x0406 {
			put32(q.Body[28:], 1)
		}
	case 0x0408, 0x0503:
		l = p.m.path
		q = controlPacket(op, 0x21, 13)
		q.Flags, q.Type, q.ID = 8, 1, p.m.relay
		put32(q.Body, 12)
		copy(q.Body[4:], p.m.material.RID[:])
		q.Body[12] = 42
	case 0x0428, 0x0523, 0x0418, 0x0513:
		l = p.m.path
		q = controlPacket(op, 0x21, 8)
		q.Flags, q.ID = 8, p.m.relay
		copy(q.Body, p.m.material.RID[:])
	default:
		p.t.Fatalf("missing reply layout for %04x", op)
	}
	var err error
	raw, err = q.Marshal()
	if err != nil {
		p.t.Fatal(err)
	}
	return typ, l, raw
}

func (p *isolationPeer) deliver(typ byte, l Link, raw []byte) []Action {
	p.t.Helper()
	if typ == 0 {
		q, err := ParsePacket(raw)
		if err != nil {
			p.t.Fatal(err)
		}
		wire, err := EncodeLegacy(q, p.m.key)
		if err != nil {
			p.t.Fatal(err)
		}
		return p.complete(p.step(Event{Kind: Wire, Link: l, Data: wire}))
	}
	prefix := 0
	if typ == 4 {
		prefix = 4
	}
	header, err := RecordHeader(typ, p.m.cfg.RecordSID, prefix+len(raw)+16)
	if err != nil {
		p.t.Fatal(err)
	}
	if prefix != 0 {
		header = append(header, make([]byte, prefix)...)
		put32(header[12:], p.serverSID())
	}
	out := p.step(Event{Kind: Wire, Link: l, Data: append(header, make([]byte, len(raw)+16)...), Marker: Marker{10, 1}})
	all := append([]Action(nil), out...)
	opens := 0
	for _, a := range out {
		if a.Kind == Open {
			opens++
			all = append(all, p.complete(p.step(Event{Kind: Decrypted, Token: a.Token, Data: raw}))...)
		}
	}
	if opens != 1 {
		p.t.Fatalf("authenticated reply admitted %d Opens, want 1", opens)
	}
	return all
}

func (p *isolationPeer) send(op uint16) []Action {
	return p.deliver(p.reply(op))
}

// A valid reply must change protocol state even after bookkeeping is excluded.
func (p *isolationPeer) accept(op uint16) {
	p.t.Helper()
	before := *p.m
	p.send(op)
	after := *p.m
	before.serial, before.discards = after.serial, after.discards
	if reflect.DeepEqual(before, after) {
		p.t.Fatal("current correlation did not advance or alter the attempt")
	}
}

func freshIsolationPeer(t *testing.T, attempt uint64, op uint16) *isolationPeer {
	t.Helper()
	p := &isolationPeer{t: t}
	if op == 0x100c {
		p.m, p.link, p.first = freshMaster(t, attempt, 7)
		return p
	}
	p.m, p.link, p.first = freshHello(t, attempt, 7)
	if op == 0x8004 || op == 0x8005 {
		return p
	}
	p.send(0x8004)
	if op == 0x0218 {
		return p
	}
	p.send(0x0218)
	if op == 0x030d || op == 0x020d || op == 1 || op == 2 {
		return p
	}
	if op == 0x030e || op == 0x0503 || op == 0x0523 || op == 0x0513 {
		p.send(0x020d)
		if op == 0x030e {
			return p
		}
		p.send(0x030e)
		p.complete(p.step(Event{Kind: Tick}))
	} else {
		p.send(0x030d)
		if op == 0x0404 || op == 0x0406 {
			p.send(0x0401)
		}
		if op >= 0x0401 && op <= 0x0406 {
			return p
		}
		p.send(0x0402)
	}
	// Give alive controls an observable counter to reset using a public Tick.
	p.now = core.Instant(time.Second)
	p.complete(p.step(Event{Kind: Tick}))
	if p.m.State() != Established || p.m.alive == 0 {
		t.Fatal("session setup did not establish and accrue liveness debt")
	}
	return p
}

func TestFreshPreviousAttemptCorrelatedReplies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		op     uint16
		offset int // Plaintext offset of the independently stale field.
		size   int
	}{
		{"master/nonce", 0x100c, 16, 2},
		{"hello4/serial", 0x8004, 32, 4}, {"hello5/serial", 0x8005, 32, 4},
		{"precheck/nonce", 0x0218, 16, 2},
		{"punch/nonce", 0x030d, 16, 2}, {"punch/RID", 0x030d, 120, 8},
		{"relay-result/RID", 0x020d, 40, 8}, {"relay-info/RID", 0x030e, 72, 8},
		{"offline/nonce", 1, 4, 2}, {"offline/server-SID", 1, 0, 4},
		{"asleep/nonce", 2, 4, 2}, {"asleep/server-SID", 2, 0, 4},
		{"knock1/RID", 0x0401, 36, 8}, {"knock2/RID", 0x0402, 36, 8},
		{"knock4/RID", 0x0404, 36, 8}, {"knock5/RID", 0x0405, 36, 8}, {"knock6/RID", 0x0406, 36, 8},
		{"direct-data/RID", 0x0408, 20, 8}, {"relay-data/RID", 0x0503, 20, 8},
		{"direct-alive/RID", 0x0428, 16, 8}, {"relay-alive/RID", 0x0523, 16, 8},
		{"direct-close/RID", 0x0418, 16, 8}, {"relay-close/RID", 0x0513, 16, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := freshIsolationPeer(t, 1, tc.op)
			_, _, previous := old.reply(tc.op)
			old.accept(tc.op) // The captured reply has authority in its own attempt.
			old.step(Event{Kind: Cancel})
			p := freshIsolationPeer(t, 2, tc.op)
			typ, l, current := p.reply(tc.op)
			stale := append([]byte(nil), current...)
			field := stale[tc.offset : tc.offset+tc.size]
			copy(field, previous[tc.offset:tc.offset+tc.size])
			if bytes.Equal(stale, current) {
				t.Fatal("previous and current correlations must differ")
			}
			// Stamp the current socket owner and authenticate in its context. Only
			// the selected old correlation differs, so a second guard cannot mask it.
			before := *p.m
			for _, a := range p.deliver(typ, l, stale) {
				if a.Kind != Open && a.Kind != ArmTimer {
					t.Fatalf("previous correlation emitted action %v", a.Kind)
				}
			}
			after := *p.m
			before.serial, before.discards = after.serial, after.discards
			if !reflect.DeepEqual(before, after) {
				t.Fatal("previous correlation changed retained protocol state")
			}
			// Positive control: the same message with current correlation still
			// has authority after rejection, including silent alive state changes.
			p.accept(tc.op)
		})
	}
}
