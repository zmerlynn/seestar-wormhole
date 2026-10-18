package iotc

import (
	"net/netip"
	"testing"
)

func TestNATObservationRules(t *testing.T) {
	var n NATTracker
	a := netip.MustParseAddrPort("192.0.2.1:10001")
	b := netip.MustParseAddrPort("192.0.2.2:10001")
	x := netip.MustParseAddrPort("198.51.100.1:40000")
	y := netip.MustParseAddrPort("198.51.100.1:40001")
	n.Observe(a, x, false)
	if n.Type() != 0 {
		t.Fatal("one observation classified")
	}
	n.Observe(b, x, false)
	if n.Type() != 2 {
		t.Fatal("same mapping")
	}
	n.Observe(a, y, false)
	if n.Type() != 0 {
		t.Fatal("changed observation did not reset")
	}
	n.Observe(b, x, false)
	if n.Type() != 3 {
		t.Fatal("differing mappings")
	}
	n.Observe(b, x, true)
	if n.Type() != 1 {
		t.Fatal("alternate source")
	}
}
