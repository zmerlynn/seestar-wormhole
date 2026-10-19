package p2ptunnel

import (
	"testing"
)

func TestC19CompleteLegacyPass(t *testing.T) {
	f, err := Decode(unhex(t, auditLegacyPass), LoginResponse)
	if err != nil {
		t.Fatalf("C19 literal decode: %v", err)
	}
	r, err := ParseLoginResult(f.Payload)
	if err != nil || r.Result != 0 || r.PortVerify != 0 || r.ServiceVerify != 0 {
		t.Fatalf("legacy result=%+v err=%v", r, err)
	}
	d := newDriver(t, DefaultConfig())
	d.step(Event{Kind: Start})
	a := d.step(Event{Kind: Stream, Data: unhex(t, auditLegacyPass)})
	if d.m.Status() != Active {
		t.Fatalf("legacy PASS status=%v actions=%+v", d.m.Status(), a)
	}
	d.open(4700)
	if d.hasWire(PortRequest) {
		t.Fatal("absent capabilities enabled verification")
	}
}
