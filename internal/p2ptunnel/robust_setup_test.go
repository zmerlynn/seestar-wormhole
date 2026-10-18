package p2ptunnel

import (
	"testing"
)

func TestC17SetupForms(t *testing.T) {
	for _, tt := range []struct {
		name, wire string
		legacy     bool
	}{{"family", auditFamilySetup, true}, {"tail", auditTailSetup, false}} {
		t.Run(tt.name, func(t *testing.T) {
			f, err := Decode(unhex(t, tt.wire), Established)
			if err != nil {
				t.Fatalf("C17 %s literal decode: %v", tt.name, err)
			}
			if _, err := f.Encode(Established); err == nil {
				t.Fatal("receive extension emitted")
			}
			s, err := ParseSetup(f.Payload)
			if err != nil || s.Port != 4700 || s.Address != ([4]byte{127, 0, 0, 1}) || s.Legacy != tt.legacy {
				t.Fatalf("setup=%+v err=%v", s, err)
			}
			cfg := DefaultConfig()
			cfg.Role = Server
			cfg.AllowedPorts = []uint16{4700}
			d := newDriver(t, cfg)
			d.step(Event{Kind: Start})
			d.step(Event{Kind: Stream, Data: unhex(t, "07000000")})
			a := d.step(Event{Kind: Stream, Data: unhex(t, tt.wire)})
			x := mustAction(t, a, ConnectLocal)
			if x.Channel != 7 || x.Port != 4700 {
				t.Fatalf("connect=%+v", x)
			}
		})
	}
}
