package p2ptunnel

import (
	"testing"
)

func TestC18CloseTail(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	for i := 1; i <= 7; i++ {
		d.open(uint16(4700 + i))
	}
	a := d.step(Event{Kind: Stream, Data: unhex(t, auditClose)})
	x, ok := find(a, CloseLocal)
	if !ok || x.Channel != 7 || x.PeerClose == nil || !x.PeerClose.HasResult || x.PeerClose.Result != 2 || d.m.Status() != Active || !d.m.Channel(1).Active || d.m.Channel(7).Active {
		t.Fatalf("C18 close: status=%v actions=%+v", d.m.Status(), a)
	}
}
