package tracerecord

import (
	"net/netip"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

func TestProjectorUsesRecorderAllowlist(t *testing.T) {
	var p Projector
	input := iotc.Event{Kind: iotc.Wire, Link: iotc.Link{Transport: iotc.UDP, Address: netip.MustParseAddrPort("192.0.2.99:54321")}, Data: []byte{4, 2, 29, 0, 4, 0, 0, 0, 12, 16, 0, 0, 0, 0, 0, 0, 'U', 'I', 'D', '!'}}
	want := Summary{Kind: uint8(iotc.Wire), Length: 20, Type: 0x100c, Peer: "peer1", PortAlias: "port1", Transport: uint8(iotc.UDP)}
	if got := p.Summarize(input); got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	input.Link.Address = netip.MustParseAddrPort("192.0.2.99:54322")
	want.PortAlias = "port2"
	if got := p.Summarize(input); got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if got := p.Summarize("secret"); !got.Unsupported {
		t.Fatal("unknown input must be unavailable")
	}
}
