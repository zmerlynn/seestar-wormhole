package iotc

import "testing"

// Routing values are builder values, not a bitfield grammar.
// Only the 0x02 refusal bit in the high byte acts.
func TestRoutingRefusalBit(t *testing.T) {
	for _, route := range []byte{0, 0x33, 0x21, 0xff} {
		for _, high := range []byte{0, 1, 4, 0x80, 0xfd, 2, 3, 0xff} {
			m, l := layoutKnockMachine(t)
			raw := unhex(layoutKnockPrefix)
			raw[10], raw[11] = route, high
			layoutWire(t, m, l, raw)
			if (m.state == Established) != (high&2 == 0) {
				t.Errorf("route %02x%02x: state %v", high, route, m.state)
			}
		}
	}
}
