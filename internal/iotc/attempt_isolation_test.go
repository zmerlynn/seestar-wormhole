package iotc

import (
	"bytes"
	"crypto/x509"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/edge"
	"net/netip"
	"testing"
	"time"
)

// Drive discovery through public events. Authentication is supplied by a mock
// edge; no endpoint/stage/serial field is installed or changed by this helper.
func freshHello(t *testing.T, attempt uint64, sid uint32, starts ...uint32) (*Machine, Link, Packet) {
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
	raw, _ := q.Marshal()
	header, _ := RecordHeader(1, sid, len(raw)+16)
	actions = step(Event{Kind: Wire, Link: Link{Address: netip.MustParseAddrPort("192.0.2.100:10240")}, Data: append(header, make([]byte, len(raw)+16)...), Marker: Marker{10, 1}})
	for _, a := range actions {
		if a.Kind == Open {
			for _, b := range step(Event{Kind: Decrypted, Token: a.Token, Data: raw}) {
				if b.Kind == Send {
					p, e := DecodeLegacy(b.Data, m.key)
					if e == nil && p.Opcode == 0x8003 {
						return m, l, p
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
