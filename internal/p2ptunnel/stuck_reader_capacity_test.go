package p2ptunnel

import (
	"bytes"
	"testing"
)

func TestTwoStuckReadersPreserveRPC(t *testing.T) {
	d := activeDriver(t, legacyCapacityConfig(), false)
	a, b, rpc := d.open(445), d.open(445), d.open(4700)
	payload := bytes.Repeat([]byte{0x17}, MaxData)
	for _, ch := range []Action{a, b} {
		for i := 0; i < (4<<20)/MaxData; i++ {
			d.frame(Frame{DataCommand, ch.Channel, payload})
		}
		if !d.m.Channel(ch.Channel).Active || !d.m.Channel(ch.Channel).SentPause {
			t.Fatal("stuck channel did not retain a valid paused burst")
		}
	}
	out := d.frame(Frame{DataCommand, rpc.Channel, []byte("rpc\n")})
	if !d.m.Channel(rpc.Channel).Active {
		t.Fatalf("two paused 4MiB backlogs closed innocent RPC: RX=%+v actions=%+v", d.m.Usage(), out)
	}
}
