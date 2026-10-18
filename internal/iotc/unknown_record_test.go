package iotc

import "testing"

// Unknown records and type-1
// opcodes are dropped after their lengths are checked, with a diagnostic only.
func TestUnknownRecordLengthValidation(t *testing.T) {
	for _, literal := range []string{"51cc00000206000007000000", "51cc000002ff010007000000aa", "51cc00000202000007000000"} {
		m, l := layoutHelloMachine(t)
		before := m.endpoints
		out := m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: unhex(literal)}, 0)
		if m.endpoints != before || m.state != Connecting || m.alive != 0 {
			t.Fatal("unknown record changed state")
		}
		if len(nonTimerEffects(out)) != 1 {
			t.Errorf("unknown record %s: expected diagnostic only, got %+v", literal, out)
		}
		bad := append(unhex(literal), 0)
		if out := m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: bad}, 0); len(nonTimerEffects(out)) != 0 {
			t.Errorf("bad envelope length logged or acted: %+v", out)
		}
	}
	for _, literal := range []string{"04021d0000000000efbe180000000000", "04021d00000000001802180000000000"} {
		m, l := layoutMasterMachine(t, UDP)
		before := m.endpoints
		out := layoutMasterIngress(t, m, l, unhex(literal), 7)
		if m.endpoints != before || m.state != Connecting {
			t.Fatal("unknown type-1 opcode changed state")
		}
		if len(nonTimerEffects(out)) != 1 {
			t.Errorf("unknown type-1 opcode needs diagnostic only: %+v", out)
		}
	}
}

func nonTimerEffects(in []Action) []Action {
	var out []Action
	for _, a := range in {
		if a.Kind != ArmTimer {
			out = append(out, a)
		}
	}
	return out
}
