package iotc

import (
	"fmt"
	"net/netip"
	"testing"
)

func TestLateDerivationAfterReady(t *testing.T) {
	m := established(t, true)
	m.state = Connecting
	l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
	m.add(endpoint{link: l, connected: true, cache: idx, stage: helloObserved})
	m.prepare(0)
	token := m.cache.Entries[idx].Pending
	m.establish(m.path, true)
	out := m.Step(Event{Kind: Derived, Attempt: 1, Token: token, Key: 3, IV: [12]byte{1}}, 0)
	for _, a := range out {
		if a.Kind == Seal {
			t.Fatalf("late derivation emitted rendezvous Seal %04x after Ready", le16(a.Data[8:]))
		}
	}
}
func TestRetiredEffectFailure(t *testing.T) {
	m := established(t, true)
	m.state = Connecting
	l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	m.pending[0] = operation{token: 4, kind: Seal, typ: 4, opcode: 0x020c, link: l, size: 64, aadN: 16}
	m.establish(m.path, true)
	m.Step(Event{Kind: EffectFailed, Attempt: 1, Token: 4, Link: l}, 0)
	if m.state != Established {
		t.Fatalf("retired relay Seal failure killed direct session: got state %v, want Established (%v)", m.state, Established)
	}
}

// A matching attempt is insufficient: success and failure must lose authority
// together when another path wins or the owner is canceled.
func TestRetiredCompletionMatrix(t *testing.T) {
	for _, kind := range []ActionKind{Resolve, Generate, OpenTCP, Derive, Seal, Open} {
		for _, cancel := range []bool{false, true} {
			for _, failed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/cancel=%t/fail=%t", kind, cancel, failed), func(t *testing.T) {
					m, _ := New(config())
					initial := m.Step(Event{Kind: Start, Attempt: 1}, 0)
					m.material = material()
					l := Link{Transport: TCP, Address: netip.MustParseAddrPort("203.0.113.1:443"), ID: 10}
					idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
					m.add(endpoint{link: l, cache: idx, connected: kind != OpenTCP, stage: helloObserved})
					e := Event{Attempt: 1, Link: l, Token: 99}
					switch kind {
					case Resolve, Generate:
						for _, a := range initial {
							if a.Kind == kind {
								e.Token = a.Token
							}
						}
						e.Link = Link{}
						if kind == Resolve {
							e.Kind = Resolved
							e.Addresses = []netip.Addr{netip.MustParseAddr("192.0.2.100")}
						} else {
							e.Kind = MaterialReady
							e.Material = material()
						}
					case OpenTCP:
						e.Kind = Connected
						e.Token = l.ID
					case Derive:
						e.Kind = Derived
						e.Link = Link{}
						e.Key = 3
						e.IV = [12]byte{8}
						m.cache.Entries[idx].Pending = 99
					case Seal:
						e.Kind = Encrypted
						e.Data = make([]byte, 80)
						m.pending[0] = operation{token: 99, kind: Seal, link: l, typ: 4, opcode: 0x020c, size: 64}
					case Open:
						e.Kind = Decrypted
						e.Data = make([]byte, 72)
						m.pending[0] = operation{token: 99, kind: Open, link: l, typ: 3, size: 72}
					}
					if cancel {
						m.Step(Event{Kind: Cancel, Attempt: 1}, 0)
					} else {
						m.establish(Link{Address: netip.MustParseAddrPort("198.51.100.1:40000")}, true)
					}
					if failed {
						e.Kind = EffectFailed
					}
					out := m.Step(e, 0)
					want := Established
					if cancel {
						want = Closed
					}
					if m.state != want {
						t.Fatalf("state=%v want=%v", m.state, want)
					}
					for _, a := range out {
						if a.Kind != ArmTimer {
							t.Fatalf("retired completion emitted %v", a.Kind)
						}
					}
					if m.cache.Count > 0 && m.cache.Entries[idx].Pending != 0 {
						t.Fatal("retained derivation token")
					}
					if cancel && m.material != (Material{}) {
						t.Fatal("retained secret material after cancellation")
					}
				})
			}
		}
	}
}

func TestEffectFailureMustMatchOwnedOperation(t *testing.T) {
	for _, kind := range []ActionKind{Seal, Open} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			m, _ := New(config())
			m.started = true
			m.state = Connecting
			l := Link{Transport: TCP, Address: netip.MustParseAddrPort("203.0.113.1:443"), ID: 10}
			m.add(endpoint{link: l, connected: true, stage: precheckPending})
			m.pending[0] = operation{token: 99, kind: kind, link: l}
			m.Step(Event{Kind: EffectFailed, Attempt: 1, Token: 98, Link: l}, 0)
			if m.state != Connecting {
				t.Fatal("unknown token was fatal")
			}
			stale := l
			stale.ID--
			m.Step(Event{Kind: EffectFailed, Attempt: 1, Token: 99, Link: stale}, 0)
			if m.state != Connecting {
				t.Fatal("wrong link generation was fatal")
			}
			m.Step(Event{Kind: EffectFailed, Attempt: 1, Token: 99, Link: l}, 0)
			if m.state != Closed {
				t.Fatal("owned effect failure ignored")
			}
		})
	}
}

func TestLateHelloCannotRestartPrecheck(t *testing.T) {
	m := established(t, true)
	m.state = Connecting
	l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
	m.add(endpoint{link: l, connected: true, cache: idx, hello: 77})
	m.establish(m.path, true)
	q := controlPacket(0x8004, 0x3f, 24)
	copy(q.Body, addressBytes(netip.MustParseAddrPort("198.51.100.1:40000")))
	put32(q.Body[16:], 77)
	put16(q.Body[20:], 7)
	b, _ := EncodeLegacy(q, m.key)
	for _, a := range m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: b}, 0) {
		if a.Kind == Derive || a.Kind == Seal {
			t.Fatal("late hello restarted discovery")
		}
	}
}

func TestAttemptWideFailureIsExplicit(t *testing.T) {
	m := established(t, true)
	m.Step(Event{Kind: EffectFailed, Attempt: 1}, 0)
	if m.state != Established {
		t.Fatal("uncorrelated effect failure killed session")
	}
	m.Step(Event{Kind: AttemptFailed, Attempt: 1}, 0)
	if m.state != Closed {
		t.Fatal("owner-wide failure ignored")
	}
}

func TestLastDerivationConsumerRetiresToken(t *testing.T) {
	for _, shared := range []bool{false, true} {
		m, _ := New(config())
		m.started = true
		m.state = Connecting
		m.material = material()
		tcp := Link{Transport: TCP, Address: netip.MustParseAddrPort("203.0.113.1:443"), ID: 10}
		idx, _ := m.cache.Install(Server{Address: tcp.Address}, Marker{10, 1})
		m.add(endpoint{link: tcp, connected: true, cache: idx})
		m.prepare(0)
		token := m.cache.Entries[idx].Pending
		if shared {
			m.add(endpoint{link: Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}, connected: true, cache: idx, stage: helloObserved})
		}
		m.selectTransport(UDP)
		if shared {
			if m.cache.Entries[idx].Pending != token {
				t.Fatal("retired a derivation still owned by UDP")
			}
		} else {
			if m.cache.Entries[idx].Pending != 0 {
				t.Fatal("retained derivation without a consumer")
			}
			m.Step(Event{Kind: EffectFailed, Attempt: 1, Token: token}, 0)
			if m.state != Connecting {
				t.Fatal("losing derivation failure killed connecting winner")
			}
		}
	}
}
