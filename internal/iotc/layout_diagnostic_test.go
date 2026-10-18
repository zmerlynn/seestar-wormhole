package iotc

import "testing"

func TestLayoutOddityActions(t *testing.T) {
	for _, family := range []string{"master-residual", "master-suffix", "knock", "hello", "alive"} {
		t.Run(family, func(t *testing.T) {
			var out []Action
			var want uint16
			switch family {
			case "master-residual", "master-suffix":
				m, l := layoutMasterMachine(t, UDP)
				literal := layoutMasterResidual
				if family == "master-suffix" {
					literal = layoutMasterSuffix
				}
				out = layoutMasterIngress(t, m, l, unhex(literal), 7)
				want = 0x100c
			case "knock":
				m, l := layoutKnockMachine(t)
				out = layoutWire(t, m, l, unhex(layoutKnockPrefix))
				want = 0x0402
			case "hello":
				m, l := layoutHelloMachine(t)
				out = layoutWire(t, m, l, unhex(layoutHelloSuffix))
				want = 0x8004
			case "alive":
				m := established(t, false)
				out = layoutWire(t, m, m.path, unhex(layoutAliveSuffix))
				want = 0x0523
			}
			count := 0
			for _, a := range out {
				if a.Kind == ReceiveOddity {
					count++
					if a.Opcode != want || len(a.Data) != 0 || a.Reason != "" || a.Link != (Link{}) {
						t.Fatalf("unsafe layout diagnostic: %+v", a)
					}
				}
			}
			if count != 1 {
				t.Fatalf("oddity actions=%d want=1", count)
			}
		})
	}
}
