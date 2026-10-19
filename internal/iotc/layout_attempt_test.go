package iotc

import (
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

// Local policy: hello serial lifetime is not established by observations,
// and there is no known on-wire attempt generation.
// Deliver old literal content on the current socket/attempt. No case relies on
// rejecting an old Event.Attempt. Exact legacy identity reuse is inherently
// indistinguishable once that source has been authorized in the new attempt.
func TestLayoutPreviousAttemptLegacy(t *testing.T) {
	for _, family := range []string{"hello4", "hello5", "knock1", "knock2", "knock3", "knock4", "knock5", "knock6", "alive", "alive-tcp"} {
		for _, same := range []bool{false, true} {
			for _, retried := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/same=%v/retried=%v", family, same, retried), func(t *testing.T) {
					old, _ := layoutHelloMachine(t)
					old.Step(Event{Kind: Cancel, Attempt: 1}, 0)
					m, l := layoutHelloMachine(t)
					m.cfg.Attempt = 2
					var raw, current []byte
					switch family {
					case "hello4", "hello5":
						raw = unhex(layoutHelloSuffix)
						if family == "hello5" {
							raw[8] = 5
						}
						current = append([]byte(nil), raw...)
						if !same {
							m.endpoints[0].hello = 78
							current[32] = 78
						}
						if retried {
							m.hello(0)
						}
					case "alive", "alive-tcp":
						m = established(t, false)
						m.cfg.Attempt = 2
						if family == "alive-tcp" {
							m.path.Transport, m.path.ID = TCP, 2
						}
						l = m.path
						raw = unhex(layoutAliveSuffix)
						current = append([]byte(nil), raw...)
						if !same {
							m.material.RID[7] ^= 1
							current[23] ^= 1
						}
						m.alive = 17
					default:
						m, l = layoutKnockMachine(t)
						m.cfg.Attempt = 2
						op := family[len(family)-1] - '0'
						raw = unhex(layoutKnockPrefix)
						raw[8] = op
						if op == 5 {
							raw = append(raw, 1)
							raw[4] = 29
						}
						if op == 6 {
							raw = append(raw, 1, 0, 0, 0)
							raw[4] = 32
						}
						current = append([]byte(nil), raw...)
						if !same {
							m.material.RID[7] ^= 1
							current[43] ^= 1
						}
						m.knockReply[0] = true
						if retried {
							m.knocks()
						}
					}
					if (family == "alive" || family == "alive-tcp") && retried {
						m.Step(Event{Kind: Tick, Attempt: 2}, core.Instant(time.Second))
						m.alive = 17
					}
					// No current source authorization: even an identical old wire identity
					// cannot introduce an endpoint, candidate, or established path.
					if family == "hello4" || family == "hello5" {
						m.endpoints[0].stage = endpointTerminal
					} else if family == "alive" || family == "alive-tcp" {
						m.state = Connecting
					} else {
						m.candidateCount = 0
					}
					layoutWire(t, m, l, raw)
					if m.nat.count != 0 || m.State() != Connecting {
						t.Fatal("old bytes created unauthorized association")
					}
					if family == "hello4" || family == "hello5" {
						m.endpoints[0].stage = endpointInitial
					} else if family == "alive" || family == "alive-tcp" {
						m.state = Established
					} else {
						m.candidateCount = 1
					}
					layoutWire(t, m, l, raw)
					progressed := m.nat.count == 1 || m.State() == Established
					if family == "alive" || family == "alive-tcp" {
						progressed = m.alive == 0
					}
					if family == "knock1" {
						progressed = m.knockReply[0]
						if !same { // Inspect the reply rather than the deliberately preauthorized final state.
							m.knockReply[0] = false
							layoutWire(t, m, l, raw)
							progressed = m.knockReply[0]
						}
					}
					if family == "knock3" {
						progressed = false
					} // No client-side transition for this direction.
					if family != "knock3" && progressed != same {
						t.Fatalf("old bytes progressed=%v same wire identity=%v", progressed, same)
					}
					// The current literal still progresses after rejection of old RID/serial.
					out := layoutWire(t, m, l, current)
					if family == "alive" || family == "alive-tcp" {
						if m.alive != 0 {
							t.Fatal("current alive rejected")
						}
					} else if family == "hello4" || family == "hello5" {
						if m.nat.count != 1 {
							t.Fatal("current hello rejected")
						}
					} else if family == "knock1" {
						if !m.knockReply[0] {
							t.Fatal("current knock rejected")
						}
					} else if family != "knock3" && m.State() != Established {
						t.Fatalf("current knock rejected: state=%v actions=%d", m.State(), len(out))
					}
				})
			}
		}
	}
}

func layoutMasterMachine(t *testing.T, transport Transport) (*Machine, Link) {
	t.Helper()
	m, l := layoutHelloMachine(t)
	l.Transport = transport
	m.endpoints[0].link, m.endpoints[0].master = l, true
	return m, l
}

// A mock authenticated-completion seam, reached through actual modern ingress.
// The plaintext is never given to Wire; token/source/type/size remain checked.
func layoutMasterIngress(t *testing.T, m *Machine, l Link, raw []byte, sid byte) []Action {
	t.Helper()
	header := unhex("51cc00000201000007000000")
	header[8] = sid
	put16(header[6:], uint16(len(raw)+16))
	out := m.Step(Event{Kind: Wire, Attempt: m.cfg.Attempt, Link: l, Data: append(header, make([]byte, len(raw)+16)...)}, 0)
	for _, a := range out {
		if a.Kind == Open {
			return m.Step(Event{Kind: Decrypted, Attempt: m.cfg.Attempt, Token: a.Token, Data: raw, Marker: Marker{10, 1}}, 0)
		}
	}
	return out
}

func TestLayoutPreviousAttemptMaster(t *testing.T) {
	for _, transport := range []Transport{UDP, TCP} {
		for _, literal := range []string{layoutMasterResidual, layoutMasterSuffix} {
			for _, changed := range []string{"nonce", "uid", "instance", "source"} {
				t.Run(fmt.Sprintf("%v/%s/%d", transport, changed, len(literal)), func(t *testing.T) {
					old, l := layoutMasterMachine(t, transport)
					old.Step(Event{Kind: Cancel, Attempt: 1}, 0)
					m, l := layoutMasterMachine(t, transport)
					m.cfg.Attempt = 2
					raw := unhex(literal)
					current := append([]byte(nil), raw...)
					sid := byte(7)
					switch changed {
					case "nonce":
						m.material.MasterNonce = 0x4322
						current[16] = 0x22
					case "uid":
						m.cfg.UID[0] = 'F'
						current[20] = 'F'
					case "instance":
						m.cfg.RecordSID = 8
					case "source":
						l.ID++
					}
					// The old plaintext is authenticated by the mock only if the current
					// record was admitted. A changed nonce/UID must still reject it afterward.
					layoutMasterIngress(t, m, l, raw, sid)
					if m.endpoints[0].stage != endpointInitial || m.count != 1 {
						t.Fatal("old master literal advanced new attempt")
					}
					l = m.endpoints[0].link
					if changed == "instance" {
						sid = 8
					}
					layoutMasterIngress(t, m, l, current, sid)
					if m.endpoints[0].stage != masterAnswered || m.cache.Count != 1 {
						t.Fatal("current master literal rejected")
					}
				})
			}
		}
	}
}
