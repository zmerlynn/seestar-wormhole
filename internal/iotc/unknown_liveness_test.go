package iotc

import "testing"

func TestUnknownOwnedPathLiveness(t *testing.T) {
	for _, family := range []string{"envelope", "authenticated-control"} {
		t.Run(family, func(t *testing.T) {
			m, l := unknownMachine(t)
			m.state, m.path, m.relay = Established, l, 0x2345
			m.endpoints[0].stage = relayInfoReady
			m.alive = 17
			var out []Action
			if family == "envelope" {
				out = m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: unhex(unknownRecordLiteral)}, 0)
			} else {
				out = unknownIngress(t, m, l, 4, unhex(unknownControlLiteral))
			}
			if m.alive != 17 || m.State() != Established || m.path != l || m.relay != 0x2345 {
				t.Fatal("unknown changed owned path or liveness")
			}
			for _, a := range out {
				if a.Kind == Open || a.Kind == Ready || a.Kind == Received {
					t.Fatal("unknown authorized work")
				}
			}
			// A subsequent correlated known alive still resets this same path.
			layoutWire(t, m, l, unhex(layoutAliveSuffix))
			if m.alive != 0 {
				t.Fatal("unknown blocked the next known alive")
			}
		})
	}
}

func TestUnknownAlertDoesNotInstallSID(t *testing.T) {
	m, l := unknownMachine(t)
	m.endpoints[0].sid = 0
	raw := unhex(unknownTriggerLiteral)
	raw[6] = 99
	unknownIngress(t, m, l, 14, raw)
	if m.State() != Connecting || m.endpoints[0].sid != 0 || m.endpoints[0].stage != rendezvousPending {
		t.Fatal("unknown alert installed authority")
	}
}

func TestUnknownFramingBounds(t *testing.T) {
	for _, mutate := range []func([]byte) []byte{
		func(b []byte) []byte { return b[:11] },
		func(b []byte) []byte { b[0] ^= 1; return b },
		func(b []byte) []byte { b[4]++; return b },
		func(b []byte) []byte { b[6]++; return b },
		func(b []byte) []byte { return append(b, 0) },
		func(b []byte) []byte {
			b = append(b, make([]byte, MaxPacket)...)
			put16(b[6:], uint16(len(b)-12))
			return b
		},
	} {
		if _, err := ParseRecord(mutate(unhex(unknownRecordLiteral))); err == nil {
			t.Fatal("malformed unknown envelope accepted")
		}
	}
	if _, err := ParseControl(Packet{Opcode: 0xffff, Body: make([]byte, MaxPacket-15)}); err == nil {
		t.Fatal("unbounded unknown control accepted")
	}
}
