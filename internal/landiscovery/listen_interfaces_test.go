//go:build unix || windows

package landiscovery

import (
	"net"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"golang.org/x/net/ipv4"
)

type fakeInterfaceSource struct {
	t         *testing.T
	iface     net.Interface
	addresses []net.Addr
	lookups   int
}

func (s *fakeInterfaceSource) interfaces() ([]net.Interface, error) {
	return []net.Interface{s.iface}, nil
}

func (s *fakeInterfaceSource) interfaceByIndex(index int) (*net.Interface, error) {
	if index != s.iface.Index {
		s.t.Fatalf("lookup index=%d, want %d", index, s.iface.Index)
	}
	s.lookups++
	return &s.iface, nil
}

func (s *fakeInterfaceSource) addrs(iface *net.Interface) ([]net.Addr, error) {
	if iface.Index != s.iface.Index {
		s.t.Fatalf("address index=%d, want %d", iface.Index, s.iface.Index)
	}
	return s.addresses, nil
}

func TestListenAllInterfaces(t *testing.T) {
	t.Parallel()
	prefix := netip.MustParsePrefix("192.168.50.2/24")
	for _, bind := range []string{"0.0.0.0", prefix.Addr().String()} {
		t.Run(bind, func(t *testing.T) {
			t.Parallel()
			originalAddresses := []net.Addr{&net.IPNet{IP: net.IPv4(192, 168, 50, 2), Mask: net.CIDRMask(24, 32)}}
			source := &fakeInterfaceSource{
				t: t, iface: net.Interface{Index: 3, Name: "synthetic-tap", Flags: net.FlagUp},
				addresses: originalAddresses,
			}
			if s, err := listenInterfaces(source, bind, 0, Options{}); err == nil {
				s.Close()
				t.Fatal("default accepted a non-broadcast adapter")
			}
			s, err := listenInterfaces(source, bind, 0, Options{AllInterfaces: true})
			if err != nil {
				t.Fatalf("opt-in Listen: %v", err)
			}
			defer s.Close()
			if want := map[int][]netip.Prefix{source.iface.Index: {prefix}}; !reflect.DeepEqual(s.interfaces, want) {
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
				source.iface.Flags, source.addresses = state.flags, state.addresses
				m, _, err := New("SN", "name", "S50")
				if err != nil {
					t.Fatal(err)
				}
				before := source.lookups
				// Exercise Serve's receive handler with synthetic ingress metadata;
				// the synthetic adapter needs no host address or route.
				out, local := s.received(m, []byte(scan), &ipv4.ControlMessage{IfIndex: source.iface.Index, Dst: net.ParseIP("192.168.50.2")},
					&net.UDPAddr{IP: net.ParseIP("192.168.50.3"), Port: 1234}, time.Duration(i)*time.Second)
				if source.lookups != before+1 || (out != nil) != state.accept {
					t.Fatalf("%s: reply=%s live lookups=%d, want reply=%t and one lookup", state.name, out, source.lookups-before, state.accept)
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
