package main

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery/interfaceio"
)

func TestProductionDiscoveryAdapterAllInterfaces(t *testing.T) {
	originalList, originalAddrs := interfaceio.Interfaces, interfaceio.Addrs
	t.Cleanup(func() {
		interfaceio.Interfaces, interfaceio.Addrs = originalList, originalAddrs
	})
	iface := net.Interface{Index: 3, Name: "synthetic-tap", Flags: net.FlagUp}
	interfaceio.Interfaces = func() ([]net.Interface, error) { return []net.Interface{iface}, nil }
	interfaceio.Addrs = func(got *net.Interface) ([]net.Addr, error) {
		if got.Index != iface.Index {
			t.Fatalf("address index=%d, want %d", got.Index, iface.Index)
		}
		return []net.Addr{&net.IPNet{IP: net.IPv4(192, 168, 50, 2), Mask: net.CIDRMask(24, 32)}}, nil
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, bind := range []string{"0.0.0.0", "192.168.50.2"} {
		t.Run(bind, func(t *testing.T) {
			if s, err := listenCommandDiscovery(bind, 0, landiscovery.Options{}, logger); err == nil {
				s.Close()
				t.Fatal("default adapter accepted a non-broadcast interface")
			}
			s, err := listenCommandDiscovery(bind, 0, landiscovery.Options{AllInterfaces: true}, logger)
			if err != nil {
				t.Fatalf("opt-in production adapter: %v", err)
			}
			defer s.Close()
			if socket, ok := s.(*landiscovery.Socket); !ok || socket.Logger != logger {
				t.Fatalf("adapter returned %T without the command logger", s)
			}
		})
	}
}
