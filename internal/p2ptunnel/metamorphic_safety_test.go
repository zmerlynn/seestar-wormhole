package p2ptunnel

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

func TestMetamorphicVerificationSafety(t *testing.T) {
	var row metamorphicFixture
	for _, r := range metamorphicFixtures(t) {
		if r.Name == "port-response" {
			row = r
		}
	}
	for _, bad := range []string{"id", "port", "denied"} {
		d := metamorphicDriver(t, row)
		b := metamorphicMutate(t, row, []byte{0xa5})
		switch bad {
		case "id":
			b[4]++
		case "port":
			b[6]++
		case "denied":
			b[8] = 0
		}
		a := metamorphicActions(t, d.step(Event{Kind: Stream, Data: b}))
		for _, x := range a {
			if x.Kind == Opened || x.Kind == ConnectLocal || x.Kind == Send {
				t.Fatalf("%s acquired channel authority: %+v", bad, x)
			}
		}
		if d.m.Usage().Channels != 0 {
			t.Fatal("verification failure allocated channel")
		}
		if bad == "id" && d.m.pending == nil {
			t.Fatal("foreign ID retired current request")
		}
		if bad != "id" && d.m.pending != nil {
			t.Fatal("refusal/mismatch failed to retire request")
		}
	}
}

func TestMetamorphicReceiveBoundaries(t *testing.T) {
	for _, row := range metamorphicFixtures(t) {
		t.Run(row.Name, func(t *testing.T) {
			// Unknown login opcodes must never become pass, irrespective of length.
			if row.Phase != Established {
				b := unhex(t, row.Wire)
				b[0] = 0xfe
				if _, err := Decode(b, row.Phase); err == nil {
					t.Fatal("unknown login accepted")
				}
				d := metamorphicDriver(t, row)
				d.step(Event{Kind: Stream, Data: b})
				if d.m.Status() != Stopped {
					t.Fatal("unknown login did not close")
				}
				return
			}
			// Maximum complete unknown is consumed, followed by the known frame in a
			// separate bounded owner event. One byte over cap fails at the header.
			d := metamorphicDriver(t, row)
			before := *d.m
			before.actions = nil
			before.decoder = Decoder{}
			unknown := append(unhex(t, "feff0020"), bytes.Repeat([]byte{0xa5}, MaxData)...)
			a := metamorphicActions(t, d.step(Event{Kind: Stream, Data: unknown}))
			after := *d.m
			after.actions = nil
			after.decoder = Decoder{}
			if len(a) != 0 || !reflect.DeepEqual(before, after) {
				t.Fatal("bounded unknown changed state/actions")
			}
			d.step(Event{Kind: Stream, Data: unhex(t, row.Wire)})
			over := bytes.Clone(unknown)
			over = append(over, 0)
			binary.LittleEndian.PutUint16(over[2:], MaxData+1)
			if _, err := Decode(over, Established); err == nil {
				t.Fatal("over-cap unknown accepted")
			}
			var decoder Decoder
			if _, _, err := decoder.Feed(over[:4], Established); err == nil {
				t.Fatal("over-cap header retained")
			}
		})
	}
	// Inconsistent legacy authentication extent and unterminated names remain
	// structural errors; adding an extension cannot hide a required byte.
	for _, tc := range []struct {
		wire  string
		phase Phase
	}{
		{"070009000008030402000000aa", LoginRequest},
		{"010733000200125c7f000001" + "0000000000000000" + "00", Established},
		{"0e0024000100" + "78787878787878787878787878787878787878787878787878787878787878787878" + "00", Established},
	} {
		if _, err := Decode(unhex(t, tc.wire), tc.phase); err == nil {
			t.Fatal("unsafe layout accepted")
		}
	}
}
