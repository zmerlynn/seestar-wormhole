package main

import (
	"io"
	"testing"
)

func TestLANDiscoveryFlags(t *testing.T) {
	o, err := parse(append([]string{"proxy"}, nil...), io.Discard)
	if err != nil || o.lanDiscovery || o.lanDiscoveryAllInterfaces {
		t.Fatal("LAN enabled by default")
	}
	for _, all := range []string{"false", "true"} {
		o, err := parse([]string{"proxy", "--bind=0.0.0.0", "--lan-discovery", "--lan-discovery-all-interfaces=" + all}, io.Discard)
		if err != nil || !o.lanDiscovery || o.lanDiscoveryAllInterfaces != (all == "true") {
			t.Fatalf("LAN all-interfaces=%s: %+v, %v", all, o, err)
		}
	}
	if _, err := parse([]string{"proxy", "--lan-discovery-all-interfaces=false"}, io.Discard); err != nil {
		t.Fatalf("explicitly disabled all-interfaces rejected: %v", err)
	}
	for _, bind := range []string{"192.168.20.2", "0.0.0.0"} {
		o, err := parse(append([]string{"proxy"}, []string{"--bind", bind, "--lan-discovery"}...), io.Discard)
		if err != nil || !o.lanDiscovery {
			t.Fatalf("LAN flags %+v, %v", o, err)
		}
	}
	for _, args := range [][]string{{"--lan-discovery"}, {"--lan-discovery", "--bind", "::"}, {"--lan-discovery", "--bind", "localhost"}, {"--lan-discovery", "--bind", "127.0.0.1"}} {
		if _, err := parse(append([]string{"proxy"}, args...), io.Discard); err == nil {
			t.Fatalf("invalid LAN flags accepted: %v", args)
		}
	}
}
