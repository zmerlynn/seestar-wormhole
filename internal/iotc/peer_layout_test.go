package iotc

import (
	"bytes"
	"fmt"
	"net/netip"
	"testing"
)

const layoutHelloSuffix = "04021d021900000004803f000000000002002711cb00710100000000000000004d00000007000000aa"
const layoutAliveSuffix = "04021d0b0900000023052400452300004433221188776655aa"

// The literal bytes are independent of the encoder; scrambling is only the
// transport envelope needed to deliver them through the real Wire ingress.
func layoutWire(t *testing.T, m *Machine, l Link, raw []byte) []Action {
	t.Helper()
	return m.Step(Event{Kind: Wire, Attempt: m.cfg.Attempt, Link: l,
		Data: Transform(raw, m.key, raw[3]&1 != 0, false)}, 0)
}

func layoutHelloMachine(t *testing.T) (*Machine, Link) {
	t.Helper()
	m, err := New(config())
	if err != nil {
		t.Fatal(err)
	}
	m.started, m.hasMaterial, m.state = true, true, Connecting
	m.material = material()
	l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
	idx, err := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
	if err != nil {
		t.Fatal(err)
	}
	m.add(endpoint{link: l, connected: true, cache: idx, hello: 77})
	return m, l
}

func TestLayoutHelloLiteral(t *testing.T) {
	for _, op := range []byte{4, 5} {
		m, l := layoutHelloMachine(t)
		raw := unhex(layoutHelloSuffix)
		raw[8] = op
		if op == 5 {
			l.Address = netip.MustParseAddrPort("203.0.113.1:10002")
		}
		out := layoutWire(t, m, l, raw)
		if m.nat.count != 1 || m.nat.records[0].observed != netip.MustParseAddrPort("203.0.113.1:10001") {
			t.Fatalf("hello %x did not record literal NAT observation: %+v", op, m.nat)
		}
		derived := false
		for _, a := range out {
			derived = derived || a.Kind == Derive
		}
		if !derived {
			t.Fatal("correlated hello did not advance precheck")
		}
	}
}

func TestLayoutRelayAliveLiteral(t *testing.T) {
	for _, transport := range []Transport{UDP, TCP} {
		m := established(t, false)
		m.path.Transport = transport
		m.alive = 17
		out := layoutWire(t, m, m.path, unhex(layoutAliveSuffix))
		if m.alive != 0 || m.State() != Established {
			t.Fatalf("relay alive state=%v age=%d", m.State(), m.alive)
		}
		for _, a := range out {
			if a.Kind == Received {
				t.Fatal("alive tail delivered as data")
			}
		}
	}
}

func layoutKnockMachine(t *testing.T) (*Machine, Link) {
	t.Helper()
	m, l := layoutHelloMachine(t)
	m.selection, m.selected = true, UDP
	m.candidates[0], m.candidateCount = l.Address, 1
	return m, l
}

func TestLayoutKnockProgressAndSafety(t *testing.T) {
	for _, op := range []byte{1, 2, 3, 4, 5, 6} {
		for _, bad := range []string{"", "uid", "rid", "source", "transport", "refused", "false", "no-prior"} {
			t.Run(fmt.Sprintf("%x/%s", op, bad), func(t *testing.T) {
				m, l := layoutKnockMachine(t)
				m.knockReply[0] = true
				raw := unhex(layoutKnockPrefix)
				raw[8] = op
				if op == 5 {
					raw = append(raw, 1)
					raw[4] = 29
				}
				if op == 6 {
					raw = append(raw, 0, 0, 0, 1)
					raw[4] = 32
				}
				switch bad {
				case "uid":
					raw[16] ^= 1
				case "rid":
					raw[43] ^= 1
				case "source":
					l.Address = netip.MustParseAddrPort("203.0.113.99:10001")
				case "transport":
					l.Transport = TCP
				case "refused":
					raw[11] |= 2
				case "false":
					if op != 5 && op != 6 {
						return
					}
					clear(raw[44:])
				case "no-prior":
					if op != 4 && op != 6 {
						return
					}
					m.knockReply[0] = false
				}
				out := layoutWire(t, m, l, raw)
				wantEstablished := bad == "" && op != 1 && op != 3
				if (m.State() == Established) != wantEstablished {
					t.Fatalf("state=%v want established=%v", m.State(), wantEstablished)
				}
				sends := 0
				for _, a := range out {
					if a.Kind != Send {
						continue
					}
					sends++
					p, err := DecodeLegacy(a.Data, m.key)
					want := "04021d022400000004043300000000004558414d504c455549443030303030303030303044332211887766550000000000080304"
					if op == 1 {
						want = "04021d022400000002043300000000004558414d504c455549443030303030303030303044332211887766550000000000080304"
					}
					decoded, _ := p.Marshal()
					if err != nil || !bytes.Equal(decoded, unhex(want)) {
						t.Fatalf("reply=%x err=%v want=%s", decoded, err, want)
					}
				}
				wantSends := 0
				if bad == "" && (op == 1 || op == 2 || op == 5) {
					wantSends = 1
				}
				if sends != wantSends {
					t.Fatalf("sends=%d want=%d", sends, wantSends)
				}
			})
		}
	}
}

func TestLayoutHelloSafety(t *testing.T) {
	for _, op := range []byte{4, 5} {
		for _, bad := range []string{"short", "serial", "instance", "source", "transport", "retired", "ambiguous", "address", "declared"} {
			m, l := layoutHelloMachine(t)
			raw := unhex(layoutHelloSuffix)
			raw[8] = op
			switch bad {
			case "short":
				raw = raw[:39]
				raw[4] = 23
			case "serial":
				raw[32] = 78
			case "instance":
				raw[36] = 8
			case "source":
				l.Address = netip.MustParseAddrPort("203.0.113.99:10001")
			case "transport":
				l.Transport = TCP
			case "retired":
				m.endpoints[0].stage = endpointTerminal
			case "ambiguous":
				if op == 4 {
					continue
				}
				m.add(endpoint{link: Link{Address: netip.MustParseAddrPort("203.0.113.2:10001")}, connected: true, hello: 77})
			case "address":
				clear(raw[18:20])
			case "declared":
				raw[4]++
			}
			out := layoutWire(t, m, l, raw)
			if m.nat.count != 0 {
				t.Fatalf("%x/%s recorded NAT", op, bad)
			}
			for _, a := range out {
				if a.Kind == Derive || a.Kind == Seal {
					t.Fatalf("%x/%s advanced", op, bad)
				}
			}
		}
	}
}

func TestLayoutAliveSafety(t *testing.T) {
	for _, transport := range []Transport{UDP, TCP} {
		for _, bad := range []string{"rid", "source", "allocation", "flag", "short", "declared", "direct-tail", "relay-close-tail", "direct-close-tail"} {
			m := established(t, false)
			m.path.Transport = transport
			l := m.path
			m.alive = 17
			raw := unhex(layoutAliveSuffix)
			switch bad {
			case "rid":
				raw[23] ^= 1
			case "source":
				l.Address = netip.MustParseAddrPort("203.0.113.99:10001")
			case "allocation":
				raw[12] ^= 1
			case "flag":
				raw[3] &^= 8
			case "short":
				raw = raw[:23]
				raw[4] = 7
			case "declared":
				raw[4]++
			case "direct-tail":
				m.direct = true
				raw[8], raw[9], raw[10] = 0x28, 4, 0x12
			case "relay-close-tail":
				raw[8] = 0x13
			case "direct-close-tail":
				m.direct = true
				raw[8], raw[9], raw[10] = 0x18, 4, 0x12
			}
			out := layoutWire(t, m, l, raw)
			if m.alive != 17 || m.State() != Established {
				t.Fatalf("%v/%s changed liveness/state", transport, bad)
			}
			for _, a := range out {
				if a.Kind == Received {
					t.Fatalf("%s delivered payload", bad)
				}
			}
		}
	}
}
