package iotc

import (
	"bytes"
	"fmt"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// Check every retained field, including candidates, cache, timers, relay
// progress, terminal state and handshake flags. Authentication may consume a
// monotonic effect token but may not acquire protocol authority.
func (p *matrixPeer) ignore(l Link, wire []byte) {
	p.t.Helper()
	before := *p.h.m
	queue := p.step(Event{Kind: Wire, Link: l, Data: wire, Marker: Marker{100, 123}})
	for len(queue) > 0 {
		a := queue[0]
		queue = queue[1:]
		switch a.Kind {
		case ArmTimer:
		case Open:
			queue = append(queue, p.step(p.open(a))...)
		default:
			p.t.Fatalf("old reply emitted %v, want only Open/ArmTimer", a.Kind)
		}
	}
	after := *p.h.m
	before.serial = after.serial
	before.discards = after.discards
	if !reflect.DeepEqual(before, after) {
		p.t.Fatal("old reply changed retained protocol state")
	}
}

func (p *matrixPeer) current(op uint16) (Link, []byte) {
	if op >= 0x0401 && op <= 0x0406 {
		return p.peerLink(), matrixKnock(p.t, op, p.knockIdentity())
	}
	return p.server, p.record(op, nil)
}
func (p *matrixPeer) progressed(op uint16) {
	p.t.Helper()
	switch op {
	case 0x0218:
		v := p.h.m.endpoints[p.h.m.find(p.server)]
		if v.sid != p.serverSID || v.stage < precheckReady {
			p.t.Fatal("valid precheck did not install current SID")
		}
	case 0x030d:
		if p.h.m.candidateCount == 0 || p.sent[0x0403] == 0 {
			p.t.Fatal("valid punch did not authorize knocks")
		}
	case 0x020d:
		if p.sent[0x0241] == 0 {
			p.t.Fatal("valid relay result did not request info")
		}
	case 0x030e:
		if p.sent[0x030f] == 0 || !p.h.m.endpoints[p.h.m.find(p.server)].relayReady {
			p.t.Fatal("valid info did not ACK/install allocation")
		}
	case 0x0401:
		if p.sent[0x0402] == 0 {
			p.t.Fatal("valid knock did not elicit response")
		}
	default:
		if p.ready != 1 || !p.h.m.direct {
			p.t.Fatal("valid knock did not establish direct path")
		}
	}
}
func (p *matrixPeer) finish(op uint16) {
	p.t.Helper()
	if p.h.m.State() == Established {
		return
	}
	if op == 0x0218 && p.transport == TCP {
		p.now += core.Instant(1500 * time.Millisecond)
		p.run(p.step(Event{Kind: Tick}))
	}
	if op == 0x030e || op == 0x020d || p.transport == TCP {
		if op == 0x0218 {
			p.deliver(p.server, p.record(0x020d, nil))
		}
		if op != 0x030e {
			p.deliver(p.server, p.record(0x030e, nil))
		}
		p.now += core.Instant(10 * time.Millisecond)
		p.run(p.step(Event{Kind: Tick}))
	} else {
		if op == 0x0218 {
			p.deliver(p.server, p.record(0x030d, nil))
		}
		p.deliver(p.peerLink(), matrixKnock(p.t, 0x0405, p.knockIdentity()))
	}
	if p.ready != 1 || p.h.m.State() != Established {
		p.t.Fatalf("replacement failed to finish: ready=%d state=%v", p.ready, p.h.m.State())
	}
}

func TestAttemptReplyMatrix(t *testing.T) {
	cases := []struct {
		op        uint16
		transport Transport
	}{
		{0x0218, UDP}, {0x0218, TCP}, {0x030d, UDP},
		{0x020d, UDP}, {0x020d, TCP}, {0x030e, UDP}, {0x030e, TCP},
		{0x0401, UDP}, {0x0402, UDP}, {0x0404, UDP}, {0x0405, UDP}, {0x0406, UDP},
	}
	for _, tc := range cases {
		for _, identity := range []string{"same-fields", "same-short-ids", "different-fields"} {
			// Full RID reuse violates the attempt contract. For unencrypted knocks it
			// is indistinguishable on the wire; the separate equivalence control below
			// records that limit instead of asserting impossible rejection.
			if tc.op >= 0x0401 && identity == "same-fields" {
				continue
			}
			for _, retry := range []bool{false, true} {
				for _, phase := range []string{"before-open", "pending-open", "after-open"} {
					if tc.op >= 0x0401 && phase == "pending-open" {
						continue
					} // Legacy knocks have no Open.
					t.Run(fmt.Sprintf("%04x/%v/%s/retry=%v/%s", tc.op, tc.transport, identity, retry, phase), func(t *testing.T) {
						old := newMatrixPeer(t, 1, identity, tc.transport)
						old.prepare(tc.op)
						_, saved := old.current(tc.op)
						// Capture a valid N reply and prove it progresses N before cancellation.
						l, _ := old.current(tc.op)
						old.deliver(l, saved)
						old.progressed(tc.op)
						old.step(Event{Kind: Cancel})
						p := newMatrixPeer(t, 2, identity, tc.transport)
						p.prepare(tc.op)
						if retry {
							request := uint16(0x020c)
							wait := 500 * time.Millisecond
							switch tc.op {
							case 0x0218:
								request = 0x0217
							case 0x030d:
								request = 0x0240
							case 0x030e:
								request = 0x0241
							}
							if tc.op >= 0x0401 {
								request = 0x0403
							}
							if tc.transport == TCP {
								wait = 2 * time.Second
							}
							count := p.sent[request]
							p.now += core.Instant(wait)
							p.run(p.step(Event{Kind: Tick}))
							if p.sent[request] <= count {
								t.Fatalf("retry did not emit %04x", request)
							}
						}
						l, current := p.current(tc.op)
						switch phase {
						case "before-open":
							p.ignore(l, saved)
							p.deliver(l, current)
						case "pending-open":
							var held Action
							for _, a := range p.step(Event{Kind: Wire, Link: l, Data: current}) {
								if a.Kind == Open {
									held = a
								} else if a.Kind != ArmTimer {
									t.Fatalf("unexpected action %v", a.Kind)
								}
							}
							if held.Token == 0 {
								t.Fatal("current Open not issued")
							}
							p.ignore(l, saved) // The old reply arrives while current authentication is pending.
							p.run(p.step(p.open(held)))
						case "after-open":
							p.deliver(l, current)
							p.ignore(l, saved)
						}
						p.progressed(tc.op)
						p.finish(tc.op)
					})
				}
			}
		}
	}
}

// A valid authenticated server can carry a foreign nonce/RID. These controls
// deliberately authenticate the literal old identity under CURRENT crypto;
// they exercise correlation after GCM, independently of stale-tag rejection.
func TestAttemptAuthenticatedIdentityMatrix(t *testing.T) {
	for _, tc := range []struct {
		op        uint16
		transport Transport
		offset    int
	}{
		{0x0218, UDP, 16}, {0x0218, TCP, 16}, {0x030d, UDP, 16}, {0x030d, UDP, 120},
		{0x020d, UDP, 40}, {0x020d, TCP, 40}, {0x030e, UDP, 72}, {0x030e, TCP, 72},
	} {
		for _, retry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%04x/%v/field=%d/retry=%v", tc.op, tc.transport, tc.offset, retry), func(t *testing.T) {
				old := newMatrixPeer(t, 1, "different-fields", tc.transport)
				old.prepare(tc.op)
				oldPlain := old.plain(tc.op)
				l, b := old.current(tc.op)
				old.deliver(l, b)
				old.progressed(tc.op)
				old.step(Event{Kind: Cancel})
				p := newMatrixPeer(t, 2, "different-fields", tc.transport)
				p.prepare(tc.op)
				if retry {
					p.now += core.Instant(2 * time.Second)
					p.run(p.step(Event{Kind: Tick}))
				}
				foreign := p.plain(tc.op)
				n := 8
				if tc.offset == 16 {
					n = 2
				}
				copy(foreign[tc.offset:tc.offset+n], oldPlain[tc.offset:tc.offset+n])
				rejected := p.rejected
				p.ignore(p.server, p.record(tc.op, foreign))
				if p.rejected != rejected {
					t.Fatal("correlation control failed authentication")
				}
				p.deliver(p.server, p.record(tc.op, nil))
				p.progressed(tc.op)
				p.finish(tc.op)
			})
		}
	}
}

func TestAttemptKnockLANAndRelay(t *testing.T) {
	for _, op := range []uint16{0x0401, 0x0402, 0x0404, 0x0405, 0x0406} {
		for _, identity := range []string{"same-short-ids", "different-fields"} {
			for _, retry := range []bool{false, true} {
				t.Run(fmt.Sprintf("%04x/%s/retry=%v", op, identity, retry), func(t *testing.T) {
					old := newMatrixPeer(t, 1, identity, UDP)
					old.prepare(op)
					saved := matrixKnock(t, op, "old")
					lan := Link{Address: netip.MustParseAddrPort("192.0.2.20:41000")}
					if op == 0x0404 || op == 0x0406 {
						old.deliver(lan, matrixKnock(t, 0x0401, "old"))
					}
					old.deliver(lan, saved)
					old.progressed(op)
					old.step(Event{Kind: Cancel})
					p := newMatrixPeer(t, 2, identity, UDP)
					p.prepare(op)
					if op == 0x0404 || op == 0x0406 {
						p.deliver(lan, matrixKnock(t, 0x0401, p.knockIdentity()))
					}
					if retry {
						p.now += core.Instant(500 * time.Millisecond)
						p.run(p.step(Event{Kind: Tick}))
					}
					p.ignore(lan, saved)
					p.deliver(lan, matrixKnock(t, op, p.knockIdentity()))
					p.progressed(op)
					p.ignore(lan, saved)
				})
			}
		}
	}
	// Knocks are direct UDP messages, even while relay negotiation runs. They
	// cannot install a direct path after relay has been selected and established.
	for _, tr := range []Transport{UDP, TCP} {
		p := newMatrixPeer(t, 2, "same-short-ids", tr)
		p.prepare(0x030e)
		p.deliver(p.server, p.record(0x030e, nil))
		p.finish(0x030e)
		p.ignore(Link{Address: netip.MustParseAddrPort("198.51.100.20:41000")}, matrixKnock(t, 0x0405, "old"))
		p.ignore(p.h.m.path, matrixKnock(t, 0x0405, "old"))
	}
}

func TestAttemptIdenticalKnockWireLimit(t *testing.T) {
	old := newMatrixPeer(t, 1, "same-fields", UDP)
	old.prepare(0x0405)
	_, saved := old.current(0x0405)
	old.step(Event{Kind: Cancel})
	p := newMatrixPeer(t, 2, "same-fields", UDP)
	p.prepare(0x0405)
	l, current := p.current(0x0405)
	if !bytes.Equal(saved, current) {
		t.Fatal("identical-RID control differs on wire")
	}
	// Counterfactual: reusing the FULL RID defeats isolation for legacy knocks.
	// There is no attempt discriminator beyond UID/RID/source in this message.
	p.deliver(l, saved)
	p.progressed(0x0405)
}

func TestAttemptReplyBeforeDerivation(t *testing.T) {
	for _, tc := range []struct {
		op uint16
		tr Transport
	}{
		{0x0218, UDP}, {0x0218, TCP}, {0x030d, UDP}, {0x020d, UDP}, {0x020d, TCP}, {0x030e, UDP}, {0x030e, TCP},
	} {
		for _, identity := range []string{"same-fields", "same-short-ids", "different-fields"} {
			for _, retry := range []bool{false, true} {
				t.Run(fmt.Sprintf("%04x/%v/%s/retry=%v", tc.op, tc.tr, identity, retry), func(t *testing.T) {
					old := newMatrixPeer(t, 1, identity, tc.tr)
					old.prepare(tc.op)
					l, saved := old.current(tc.op)
					old.deliver(l, saved)
					old.progressed(tc.op)
					old.step(Event{Kind: Cancel})
					p := newMatrixPeer(t, 2, identity, tc.tr, true)
					if len(p.derivations) != 1 || p.sent[0x0217] != 0 {
						t.Fatal("not suspended before key derivation")
					}
					if retry {
						p.now += core.Instant(2 * time.Second)
						p.run(p.step(Event{Kind: Tick}))
					}
					p.ignore(p.server, saved)
					p.holdDerive = false
					p.run(p.derivations)
					p.prepare(tc.op)
					l, b := p.current(tc.op)
					p.deliver(l, b)
					p.progressed(tc.op)
					p.finish(tc.op)
				})
			}
		}
	}
}

func TestAttemptStaleOpenAcrossRetry(t *testing.T) {
	for _, tc := range []struct {
		op uint16
		tr Transport
	}{
		{0x0218, UDP}, {0x0218, TCP}, {0x030d, UDP}, {0x020d, UDP}, {0x020d, TCP}, {0x030e, UDP}, {0x030e, TCP},
	} {
		t.Run(fmt.Sprintf("%04x/%v", tc.op, tc.tr), func(t *testing.T) {
			old := newMatrixPeer(t, 1, "same-fields", tc.tr)
			old.prepare(tc.op)
			l, saved := old.current(tc.op)
			old.deliver(l, saved)
			old.progressed(tc.op)
			old.step(Event{Kind: Cancel})
			p := newMatrixPeer(t, 2, "same-fields", tc.tr)
			p.prepare(tc.op)
			var held Action
			for _, a := range p.step(Event{Kind: Wire, Link: p.server, Data: saved}) {
				if a.Kind == Open {
					held = a
				} else if a.Kind != ArmTimer {
					t.Fatalf("old wire emitted %v", a.Kind)
				}
			}
			if held.Token == 0 {
				t.Fatal("no old-ciphertext authentication work")
			}
			p.now += core.Instant(2 * time.Second)
			p.run(p.step(Event{Kind: Tick}))
			l, b := p.current(tc.op)
			// A current reply may be dropped while authentication is busy. After the
			// stale tag fails, the same valid reply must be admissible, without reset.
			before := *p.h.m
			p.ignore(l, b)
			e := p.open(held)
			if e.Kind != CryptoRejected {
				t.Fatal("old cipher authenticated under new attempt key")
			}
			for _, a := range p.step(e) {
				if a.Kind != ArmTimer {
					t.Fatalf("tag failure emitted %v", a.Kind)
				}
			}
			after := *p.h.m
			before.discards = after.discards
			for i, pending := range before.pending {
				if pending.token == held.Token {
					before.pending[i] = operation{}
				}
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("late stale authentication changed protocol authority")
			}
			p.deliver(l, b)
			p.progressed(tc.op)
			p.finish(tc.op)
		})
	}
}

func TestAttemptRecoveryAfterEstablishment(t *testing.T) {
	for _, tc := range []struct {
		op uint16
		tr Transport
	}{{0x030d, UDP}, {0x030e, UDP}, {0x030e, TCP}} {
		for _, identity := range []string{"same-fields", "same-short-ids", "different-fields"} {
			t.Run(fmt.Sprintf("%04x/%v/%s", tc.op, tc.tr, identity), func(t *testing.T) {
				old := newMatrixPeer(t, 1, identity, tc.tr)
				old.prepare(tc.op)
				l, saved := old.current(tc.op)
				old.deliver(l, saved)
				old.progressed(tc.op)
				old.finish(tc.op)
				old.step(Event{Kind: Cancel})
				p := newMatrixPeer(t, 2, identity, tc.tr)
				p.prepare(tc.op)
				l, current := p.current(tc.op)
				p.deliver(l, current)
				p.finish(tc.op)
				p.ignore(l, saved)
				request := uint16(0x030f)
				if tc.op == 0x030d {
					request = 0x0403
				}
				before := p.sent[request]
				p.deliver(l, current)
				if p.sent[request] != before+1 || p.ready != 1 {
					t.Fatalf("current recovery sends=%d want=%d ready=%d", p.sent[request], before+1, p.ready)
				}
			})
		}
	}
}

func TestAttemptAuthenticatedSessionMatrix(t *testing.T) {
	for _, tc := range []struct {
		op uint16
		tr Transport
	}{
		{0x0218, UDP}, {0x0218, TCP}, {0x030d, UDP}, {0x020d, UDP}, {0x020d, TCP}, {0x030e, UDP}, {0x030e, TCP},
	} {
		for _, field := range []string{"record-SID", "server-SID"} {
			if field == "server-SID" && tc.op == 0x0218 {
				continue
			} // Precheck installs this SID.
			t.Run(fmt.Sprintf("%04x/%v/%s", tc.op, tc.tr, field), func(t *testing.T) {
				old := newMatrixPeer(t, 1, "different-fields", tc.tr)
				old.prepare(tc.op)
				l, saved := old.current(tc.op)
				old.deliver(l, saved)
				old.progressed(tc.op)
				old.step(Event{Kind: Cancel})
				p := newMatrixPeer(t, 2, "different-fields", tc.tr)
				p.prepare(tc.op)
				b := p.record(tc.op, nil)
				prefix := 16
				if tc.op == 0x0218 {
					prefix = 12
				}
				if field == "record-SID" {
					copy(b[8:12], saved[8:12])
				} else {
					copy(b[12:16], saved[12:16])
				}
				iv := p.h.ivs[3]
				b = append(b[:prefix], p.h.keys[3].Seal(nil, iv[:], p.plain(tc.op), b[:prefix])...)
				rejected := p.rejected
				p.ignore(p.server, b)
				if p.rejected != rejected {
					t.Fatal("session control failed authentication")
				}
				p.deliver(p.server, p.record(tc.op, nil))
				p.progressed(tc.op)
				p.finish(tc.op)
			})
		}
	}
}
