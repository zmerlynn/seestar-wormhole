package iotc

import (
	"bytes"
	"net/netip"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestUnknownModernSafety(t *testing.T) {
	for _, family := range []string{"alert", "result", "control"} {
		for _, bad := range []string{"", "attempt", "source", "instance", "server-sid", "nonce", "uid", "rid", "short", "token", "rejected", "size"} {
			t.Run(family+"/"+bad, func(t *testing.T) {
				m, l := unknownMachine(t)
				plain, typ := unhex(unknownResultLiteral), byte(4)
				copy(plain[36:40], unhex("ffffffff")) // Known refusal must still be fatal.
				if family == "alert" {
					plain, typ = unhex(unknownTriggerLiteral), 14
				}
				if family == "control" {
					plain = unhex(unknownControlLiteral)
				}
				header := unhex("51cc0000020400000700000055000000")
				header[5] = typ
				if typ == 14 {
					header = header[:12]
				}
				switch bad {
				case "server-sid":
					if typ == 14 {
						plain[0] ^= 1
					} else {
						header[12] ^= 1
					}
				case "nonce":
					if typ != 14 {
						return
					}
					plain[4] ^= 1
				case "uid":
					if family != "result" {
						return
					}
					plain[16] ^= 1
				case "rid":
					if family != "result" {
						return
					}
					plain[40] ^= 1
				case "short":
					plain = plain[:len(plain)-1]
				case "instance":
					header[8] ^= 1
				case "source":
					l.ID++
				}
				put16(header[6:], uint16(len(header)-12+len(plain)+16))
				e := Event{Kind: Wire, Attempt: 1, Link: l, Data: append(header, make([]byte, len(plain)+16)...)}
				if bad == "attempt" {
					e.Attempt++
				}
				out := m.Step(e, 0)
				for _, a := range out {
					if a.Kind != Open {
						continue
					}
					completion := Event{Kind: Decrypted, Attempt: 1, Token: a.Token, Data: plain}
					if bad == "token" {
						completion.Token++
					}
					if bad == "rejected" {
						m.Step(Event{Kind: CryptoRejected, Attempt: 1, Token: a.Token}, 0)
					}
					if bad == "size" {
						completion.Data = append(append([]byte(nil), plain...), 0)
					}
					out = m.Step(completion, 0)
					break
				}
				if bad == "" && family != "control" {
					if m.State() != Closed {
						t.Fatal("known refusal lost")
					}
					return
				}
				if m.State() != Connecting || m.endpoints[0].stage != rendezvousPending || m.RelayProgress() != (RelayProgress{}) {
					t.Fatal("untrusted completion changed outcome")
				}
				count := 0
				for _, a := range out {
					if a.Kind == ReceiveOddity {
						count++
					}
				}
				want := 0
				if bad == "" {
					want = 1
				}
				if count != want {
					t.Fatalf("oddities=%d want=%d", count, want)
				}
			})
		}
	}
}

func TestUnknownRelayOutcomesAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		hex    string
		reason FailureReason
	}{
		{"ffffffff", DeviceRefused}, {"feffffff", ServerRefused}, {"fdffffff", DeviceUnknown},
		{"fcffffff", AnotherRelayRequest}, {"fbffffff", RelayUnsupported}, {"faffffff", MultipleDeviceLogins},
		{"f8ffffff", DeviceAsleep}, {"f7ffffff", PortRejected}, {"eaffffff", AuthenticationRejected}, {"e9ffffff", AuthenticationUnsupported},
	} {
		m, l := unknownMachine(t)
		raw := unhex(unknownResultLiteral)
		copy(raw[36:40], unhex(tc.hex))
		out := unknownIngress(t, m, l, 4, raw)
		failed := false
		for _, a := range out {
			if a.Kind == Failed {
				failed = a.Failures == tc.reason
			}
		}
		if m.State() != Closed || !failed {
			t.Fatalf("known result %s lost refusal %v", tc.hex, tc.reason)
		}
	}
	for _, value := range []string{"7b000000", "9dffffff", "00000080", "ffffff7f"} {
		m, l := unknownMachine(t)
		raw := unhex(unknownResultLiteral)
		copy(raw[36:40], unhex(value))
		for range 3 {
			unknownIngress(t, m, l, 4, raw)
		}
		if m.State() != Connecting || m.RelayProgress() != (RelayProgress{}) {
			t.Fatal("unknown granted outcome")
		}
		out := m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(60*time.Second))
		failed := false
		for _, a := range out {
			if a.Kind == Failed {
				failed = a.Failures == ConnectionTimeout
			}
		}
		if m.State() != Closed || !failed {
			t.Fatal("unknown bypassed deadline or invented refusal")
		}
	}
	for _, value := range []string{"00000000", "f9ffffff"} {
		m, l := unknownMachine(t)
		raw := unhex(unknownResultLiteral)
		copy(raw[36:40], unhex(value))
		unknownIngress(t, m, l, 4, raw)
		want := rendezvousPending
		if value == "00000000" {
			want = relayInfoPending
		}
		if m.State() != Connecting || m.endpoints[0].stage != want {
			t.Fatal("known success/pending changed")
		}
	}
}

func TestUnknownOpaqueRouteSafety(t *testing.T) {
	for _, family := range []string{"data", "knock"} {
		for _, bad := range []string{"uid", "rid", "source", "transport", "refusal", "channel", "datatype", "extent", "direction", "length"} {
			t.Run(family+"/"+bad, func(t *testing.T) {
				m, l := layoutKnockMachine(t)
				raw := unhex(opaqueKnockLiteral)
				if family == "data" {
					m = established(t, true)
					l, raw = m.path, unhex(opaqueDataLiteral)
					m.alive = 17
				}
				switch bad {
				case "uid":
					if family != "knock" {
						return
					}
					raw[16] ^= 1
				case "rid":
					if family == "data" {
						raw[20] ^= 1
					} else {
						raw[36] ^= 1
					}
				case "source":
					l.Address = netip.MustParseAddrPort("192.0.2.99:1234")
				case "transport":
					l.Transport = TCP
				case "refusal":
					if family != "knock" {
						return
					}
					raw[11] |= 2
				case "channel":
					if family != "data" {
						return
					}
					raw[14] = 1
				case "datatype":
					if family != "data" {
						return
					}
					raw[15] = 0
				case "extent":
					if family != "data" {
						return
					}
					raw[16] = 13
				case "direction":
					raw[8] = 0 // Outside the received data/knock opcode catalog.
				case "length":
					raw = raw[:len(raw)-1]
				}
				out := layoutWire(t, m, l, raw)
				if family == "data" && m.alive != 17 || family == "knock" && m.State() != Connecting {
					t.Fatal("unsafe route input advanced path")
				}
				for _, a := range out {
					if a.Kind == Received || a.Kind == Ready || a.Kind == ReceiveOddity {
						t.Fatal("unsafe route input delivered/observed")
					}
				}
			})
		}
	}
}

func TestUnknownStreamAndLiveness(t *testing.T) {
	unknown := unhex(unknownRecordLiteral)
	known := unhex("51cc0000020510000700000000000000000000000000000000000000")
	stream := append(append([]byte(nil), unknown...), known...)
	for split := 0; split <= len(stream); split++ {
		var d Decoder
		var frames [][]byte
		for _, chunk := range [][]byte{stream[:split], stream[split:]} {
			got, err := d.Feed(chunk, [16]byte{})
			if err != nil {
				t.Fatal(err)
			}
			frames = append(frames, got...)
		}
		if len(frames) != 2 || !bytes.Equal(frames[0], unknown) || !bytes.Equal(frames[1], known) {
			t.Fatalf("split=%d lost frame", split)
		}
	}
	for _, typ := range []byte{0, 2, 5, 16, 23, 99, 255} {
		m, l := unknownMachine(t)
		raw := unhex(unknownRecordLiteral)
		raw[5] = typ
		out := m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: raw}, 0)
		for _, a := range out {
			if a.Kind == Open || a.Kind == Ready || a.Kind == Received {
				t.Fatalf("type=%d interpreted", typ)
			}
		}
		// Unknown envelopes cannot block the next authenticated known refusal.
		plain := unhex(unknownResultLiteral)
		copy(plain[36:40], unhex("ffffffff"))
		unknownIngress(t, m, l, 4, plain)
		if m.State() != Closed {
			t.Fatalf("type=%d blocked next known frame", typ)
		}
		m = established(t, true)
		m.alive = 17
		m.Step(Event{Kind: Wire, Attempt: 1, Link: m.path, Data: raw}, 0)
		if m.alive != 17 || m.State() != Established {
			t.Fatalf("type=%d affected liveness", typ)
		}
	}
}
