package rdt

import "testing"

// A complete fifth range is still outside the four-range defensive bound;
// inactive counts have no effect.
func TestSACKRangeCapacity(t *testing.T) {
	if _, err := DecodeSACK(unhex(t, auditFiveSACK)); err == nil {
		t.Error("accepted five ranges")
	}
	for _, literal := range []string{"0000000000000000", "00000000ffffffff", "0000000000000080"} {
		m := newMachine(t, nil)
		ready(t, m)
		before := m.Usage()
		m.Step(Event{Kind: Receive, Packet: Packet{Type: SACK2, ID: 7, Payload: unhex(t, literal)}}, 0)
		if m.Usage() != before || m.Status() != Established {
			t.Fatal("inactive signed count changed state")
		}
	}
	for _, literal := range []string{auditCompactSACK, "00000000020000000100000000000000010000000000000002000000000000000200000000000000", "000000000400000001000000000000000100000000000000020000000000000002000000000000000300000000000000030000000000000004000000000000000400000000000000"} {
		b := unhex(t, literal)
		if _, err := DecodeSACK(b); err != nil {
			t.Errorf("complete range array rejected: %v", err)
		}
		if _, err := DecodeSACK(b[:len(b)-1]); err == nil {
			t.Error("accepted missing range byte")
		}
	}
	if _, err := DecodeSACK(unhex(t, "000000000100000008000000010000000300000001000000")); err == nil {
		t.Error("accepted reversed range")
	}
}
