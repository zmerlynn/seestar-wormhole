package p2ptunnel

import (
	"bytes"
	"reflect"
	"testing"
)

func TestC20UnusedSessionChannel(t *testing.T) {
	for _, tt := range []struct {
		name, wire string
		phase      Phase
	}{{"pass", auditChannelPass, LoginResponse}, {"port", auditChannelPort, Established}, {"service", auditChannelService, Established}} {
		t.Run(tt.name, func(t *testing.T) {
			b := unhex(t, tt.wire)
			f, err := Decode(b, tt.phase)
			if err != nil {
				t.Fatalf("C20 %s literal decode: %v", tt.name, err)
			}
			if f.Channel != 7 {
				t.Fatalf("channel=%d", f.Channel)
			}
			// Session messages cannot use the header as a data-channel identity.
			run := func(ch byte) (Status, Usage, []Action) {
				d := newDriver(t, DefaultConfig())
				d.step(Event{Kind: Start})
				if tt.phase == Established {
					d.step(Event{Kind: Stream, Data: unhex(t, "080008000000000001000000")})
					d.step(Event{Kind: Open, Port: 4700, Request: 23})
					d.m.pending.id = 0x1234
				}
				wire := bytes.Clone(b)
				wire[1] = ch
				a := d.step(Event{Kind: Stream, Data: wire})
				filtered := a[:0]
				for _, x := range a {
					if x.Kind != ReceiveOddity {
						filtered = append(filtered, x)
					}
				}
				a = filtered
				if d.m.Channel(7).Active {
					t.Fatal("session header allocated channel 7")
				}
				return d.m.Status(), d.m.Usage(), a
			}
			s0, u0, a0 := run(0)
			s7, u7, a7 := run(7)
			if s0 != s7 || u0 != u7 || !reflect.DeepEqual(a0, a7) {
				t.Fatalf("channel changed session semantics: %v/%v %+v/%+v", s0, s7, a0, a7)
			}
		})
	}
}
