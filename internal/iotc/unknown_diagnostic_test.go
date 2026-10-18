package iotc

import "testing"

func TestUnknownLiteralDiagnostics(t *testing.T) {
	for _, family := range []string{"record", "control", "alert-code", "alert-trigger", "relay-result", "route", "master-unit"} {
		t.Run(family, func(t *testing.T) {
			m, l := unknownMachine(t)
			var out []Action
			var category Oddity
			var opcode uint16
			var typ byte
			switch family {
			case "record":
				category, typ = UnknownRecord, 99
				out = m.Step(Event{Kind: Wire, Attempt: 1, Link: l, Data: unhex(unknownRecordLiteral)}, 0)
			case "control":
				category, opcode, typ = UnknownControl, 0xffff, 4
				out = unknownIngress(t, m, l, 4, unhex(unknownControlLiteral))
			case "alert-code":
				category, typ = UnknownAlert, 14
				raw := unhex(unknownTriggerLiteral)
				raw[6], raw[7] = 99, 4
				out = unknownIngress(t, m, l, 14, raw)
			case "relay-result":
				category, opcode, typ = UnknownRelayResult, 0x020d, 4
				out = unknownIngress(t, m, l, 4, unhex(unknownResultLiteral))
			case "alert-trigger":
				category, typ = AlertTrigger, 9
				out = unknownIngress(t, m, l, 14, unhex(unknownTriggerLiteral))
			case "route":
				category, opcode = OpaqueRoute, 0x0408
				m = established(t, true)
				out = layoutWire(t, m, m.path, unhex(opaqueDataLiteral))
			case "master-unit":
				category, opcode, typ = UnknownMasterUnit, 0x100c, 1
				m, l = layoutMasterMachine(t, UDP)
				raw := unhex(layoutMasterSuffix)
				raw[76] = 99 // Complete length-delimited unit, no invented interpretation.
				raw = raw[:len(raw)-1]
				raw[4]-- // Remove the unrelated layout suffix.
				out = layoutMasterIngress(t, m, l, raw, 7)
			}
			count := 0
			for _, a := range out {
				if a.Kind == ReceiveOddity {
					count++
					if a.Oddity != category || a.Opcode != opcode || a.Type != typ || len(a.Data) != 0 || len(a.AAD) != 0 || a.Reason != "" || a.Domain != "" || a.Key != 0 || a.Private != 0 || a.Link != (Link{}) {
						t.Fatal("unexpected category or secret-bearing diagnostic")
					}
				}
			}
			if count != 1 {
				t.Fatalf("oddity actions=%d want=1", count)
			}
		})
	}
}
