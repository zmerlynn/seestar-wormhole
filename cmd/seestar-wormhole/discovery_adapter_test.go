//go:build unix || windows

package main

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

type commandInterfaces struct {
	t         *testing.T
	iface     net.Interface
	listCalls int
}

func (s *commandInterfaces) List() ([]net.Interface, error) {
	s.listCalls++
	return []net.Interface{s.iface}, nil
}

func (s *commandInterfaces) ByIndex(index int) (*net.Interface, error) {
	if index != s.iface.Index {
		s.t.Fatalf("lookup index=%d, want %d", index, s.iface.Index)
	}
	return &s.iface, nil
}

func (s *commandInterfaces) Addrs(iface *net.Interface) ([]net.Addr, error) {
	if iface.Index != s.iface.Index {
		s.t.Fatalf("address index=%d, want %d", iface.Index, s.iface.Index)
	}
	return []net.Addr{&net.IPNet{IP: net.IPv4(192, 0, 2, 2), Mask: net.CIDRMask(24, 32)}}, nil
}

func TestProductionDiscoveryAdapterAllInterfaces(t *testing.T) {
	t.Parallel()
	source := &commandInterfaces{
		t: t, iface: net.Interface{Index: 3, Name: "synthetic-tap", Flags: net.FlagUp},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	options := landiscovery.Options{AllInterfaces: true, Interfaces: source}
	discovery, err := listenCommandDiscovery("0.0.0.0", 0, options, logger)
	if err != nil {
		t.Fatalf("opt-in command discovery: %v", err)
	}
	defer discovery.Close()
	if source.listCalls != 1 {
		t.Fatalf("interface lists=%d, want 1", source.listCalls)
	}
	socket, ok := discovery.(*landiscovery.Socket)
	if !ok {
		t.Fatalf("command discovery type=%T, want *landiscovery.Socket", discovery)
	}
	if socket.Logger != logger {
		t.Fatal("command discovery did not attach the supplied logger")
	}
	options.AllInterfaces = false
	if discovery, err := listenCommandDiscovery("0.0.0.0", 0, options, logger); err == nil {
		discovery.Close()
		t.Fatal("default command discovery accepted a non-broadcast adapter")
	}
	if source.listCalls != 2 {
		t.Fatalf("interface lists=%d, want 2", source.listCalls)
	}
}
