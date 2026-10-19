//go:build unix || windows

package landiscovery

import (
	"net"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery/interfaceio"
	"golang.org/x/net/ipv4"
)

func TestListenAllInterfaces(t *testing.T) {
	originalList, originalLookup, originalAddrs := interfaceio.Interfaces, interfaceio.InterfaceByIndex, interfaceio.Addrs
	t.Cleanup(func() {
		interfaceio.Interfaces, interfaceio.InterfaceByIndex, interfaceio.Addrs = originalList, originalLookup, originalAddrs
	})
	iface := net.Interface{Index: 3, Name: "synthetic-tap", Flags: net.FlagUp}
	prefix := netip.MustParsePrefix("192.168.50.2/24")
	originalAddresses := []net.Addr{&net.IPNet{IP: net.IPv4(192, 168, 50, 2), Mask: net.CIDRMask(24, 32)}}
	addresses := originalAddresses
	lookups := 0
	interfaceio.Interfaces = func() ([]net.Interface, error) { return []net.Interface{iface}, nil }
	interfaceio.InterfaceByIndex = func(index int) (*net.Interface, error) {
		if index != iface.Index {
			t.Fatalf("lookup index=%d, want %d", index, iface.Index)
		}
		lookups++
		return &iface, nil
	}
	interfaceio.Addrs = func(got *net.Interface) ([]net.Addr, error) {
		if got.Index != iface.Index {
			t.Fatalf("address index=%d, want %d", got.Index, iface.Index)
		}
		return addresses, nil
	}
	for _, bind := range []string{"0.0.0.0", prefix.Addr().String()} {
		t.Run(bind, func(t *testing.T) {
			if s, err := Listen(bind, 0, Options{}); err == nil {
				s.Close()
				t.Fatal("default accepted a non-broadcast adapter")
			}
			s, err := Listen(bind, 0, Options{AllInterfaces: true})
			if err != nil {
				t.Fatalf("opt-in Listen: %v", err)
			}
			defer s.Close()
			if want := map[int][]netip.Prefix{iface.Index: {prefix}}; !reflect.DeepEqual(s.interfaces, want) {
				t.Fatalf("selected=%v, want %v", s.interfaces, want)
			}
			for i, state := range []struct {
				name      string
				flags     net.Flags
				addresses []net.Addr
				accept    bool
			}{
				{"up", net.FlagUp, originalAddresses, true},
				{"still up", net.FlagUp, originalAddresses, true},
				{"down", 0, originalAddresses, false},
				{"loopback", net.FlagUp | net.FlagLoopback, originalAddresses, false},
				{"address removed", net.FlagUp, nil, false},
				{"address replaced", net.FlagUp, []net.Addr{&net.IPNet{IP: net.IPv4(192, 168, 50, 4), Mask: net.CIDRMask(24, 32)}}, false},
				{"restored", net.FlagUp, originalAddresses, true},
			} {
				iface.Flags, addresses = state.flags, state.addresses
				m, _, err := New("SN", "name", "S50")
				if err != nil {
					t.Fatal(err)
				}
				before := lookups
				// Exercise Serve's receive handler with synthetic ingress metadata;
				// the synthetic adapter needs no host address or route.
				out, local := s.received(m, []byte(scan), &ipv4.ControlMessage{IfIndex: iface.Index, Dst: net.ParseIP("192.168.50.2")},
					&net.UDPAddr{IP: net.ParseIP("192.168.50.3"), Port: 1234}, time.Duration(i)*time.Second)
				if lookups != before+1 || (out != nil) != state.accept {
					t.Fatalf("%s: reply=%s live lookups=%d, want reply=%t and one lookup", state.name, out, lookups-before, state.accept)
				}
				if state.accept {
					var r reply
					if err := json.Unmarshal(out, &r); err != nil || string(r.ID) != "7" || r.Code != 0 || r.Result == nil || local != prefix.Addr() {
						t.Fatalf("%s: reply=%s source=%v err=%v", state.name, out, local, err)
					}
				}
			}
		})
	}
}
