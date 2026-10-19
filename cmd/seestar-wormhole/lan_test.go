package main

import (
	"io"
	"runtime"
	"testing"
)

func TestLANDiscoveryFlags(t *testing.T) {
	o, err := parse(append([]string{"proxy"}, nil...), io.Discard)
	if err != nil || o.lanDiscovery {
		t.Fatal("LAN enabled by default")
	}
	for _, bind := range []string{"192.168.20.2", "0.0.0.0"} {
		o, err := parse(append([]string{"proxy"}, []string{"--bind", bind, "--lan-discovery"}...), io.Discard)
		if runtime.GOOS == "windows" {
			if err == nil {
				t.Fatal("Windows discovery accepted")
			}
			continue
		}
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
