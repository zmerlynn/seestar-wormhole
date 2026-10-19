package main

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

func TestWildcardBindExposure(t *testing.T) {
	for _, tc := range []struct {
		name, iface, address, bind string
		flags                      net.Flags
		warn                       bool
	}{
		{"linux LAN", "eth0", "192.168.1.10/24", "0.0.0.0", net.FlagUp, false},
		{"mac LAN", "en0", "10.0.0.10/24", "::", net.FlagUp, false},
		{"private 172", "eth0", "172.31.255.1/16", "::", net.FlagUp, false},
		{"ULA", "en0", "fd12::10/64", "::", net.FlagUp, false},
		{"v4 loopback", "lo", "127.0.0.1/8", "::", net.FlagUp, false},
		{"v6 loopback", "lo0", "::1/128", "::", net.FlagUp, false},
		{"v4 link local", "en0", "169.254.1.2/16", "::", net.FlagUp, false},
		{"v6 link local", "en0", "fe80::1%en0", "::", net.FlagUp, false},
		{"CGNAT lower boundary", "eth0", "100.64.0.1/10", "0.0.0.0", net.FlagUp, true},
		{"CGNAT upper boundary", "en0", "100.127.255.254/10", "::", net.FlagUp, true},
		{"global v4", "eth0", "8.8.8.8/24", "0.0.0.0", net.FlagUp, true},
		{"global v6", "en0", "2606:4700::1/64", "::", net.FlagUp, true},
		{"v4 wildcard excludes v6", "en0", "2606:4700::1/64", "0.0.0.0", net.FlagUp, false},
		{"mapped private", "eth0", "::ffff:192.168.1.10/120", "::", net.FlagUp, false},
		{"mapped CGNAT", "eth0", "::ffff:100.64.0.1/106", "::", net.FlagUp, true},
		{"tailscale private v4", "tailscale0", "10.0.0.1/32", "::", net.FlagUp, true},
		{"tailscale ULA", "tailscale0", "fd7a:115c:a1e0::1/128", "::", net.FlagUp, true},
		{"mac tunnel private", "utun3", "192.168.1.2/32", "::", net.FlagUp, true},
		{"mac tunnel ULA", "utun3", "fd12::2/64", "::", net.FlagUp, true},
		{"wireguard private", "wg0", "172.16.1.1/24", "0.0.0.0", net.FlagUp, true},
		{"linux tun", "tun0", "10.1.1.1/24", "::", net.FlagUp, true},
		{"linux tap", "tap0", "10.1.1.1/24", "::", net.FlagUp, true},
		{"renamed point to point", "vpn0", "10.1.1.1/24", "::", net.FlagUp | net.FlagPointToPoint, true},
		{"tunnel link local", "utun3", "fe80::1%utun3", "::", net.FlagUp, false},
		{"down interface", "tailscale0", "100.64.0.1/10", "::", 0, false},
		{"multicast", "eth0", "224.0.0.1/24", "::", net.FlagUp, false},
		{"unspecified", "eth0", "0.0.0.0/0", "::", net.FlagUp, false},
		{"explicit LAN", "tailscale0", "100.64.0.1/10", "192.168.1.10", net.FlagUp, false},
		{"explicit public", "eth0", "8.8.8.8/24", "8.8.8.8", net.FlagUp, false},
		{"explicit IPv6", "utun3", "fd12::2/64", "fd12::2", net.FlagUp, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			source := interfaceSource{
				interfaces: func() ([]net.Interface, error) {
					calls++
					return []net.Interface{{Name: tc.iface, Flags: tc.flags}}, nil
				},
				addrs: func(net.Interface) ([]net.Addr, error) {
					return []net.Addr{interfaceAddr(tc.address)}, nil
				},
			}
			var logs bytes.Buffer
			addr := &net.TCPAddr{IP: net.ParseIP(tc.bind), Port: 4700}
			warnWildcardBind(newLogger("json", &logs), []proxy.Forward{boundTestForward(addr, 4700)}, source)
			if got := logs.Len() > 0; got != tc.warn {
				t.Fatalf("warning=%v, want %v; logs=%s", got, tc.warn, &logs)
			}
			if !addr.IP.IsUnspecified() && calls != 0 {
				t.Fatalf("explicit bind enumerated interfaces %d times, want 0", calls)
			}
			if tc.warn {
				var record struct {
					Level, Msg, Advice   string
					Listeners, Exposures []string
				}
				if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Fatal(err)
				}
				if record.Level != "WARN" || record.Msg != "wildcard_bind_warning" || !reflect.DeepEqual(record.Listeners, []string{"4700:4700"}) || !strings.Contains(record.Advice, "explicit LAN bind") {
					t.Fatalf("incomplete warning: %s", &logs)
				}
				if len(record.Exposures) != 1 || !strings.Contains(record.Exposures[0], tc.iface) || !strings.Contains(record.Exposures[0], strings.Split(tc.address, "/")[0]) {
					t.Fatalf("missing interface/address: %s", &logs)
				}
			}
		})
	}
}

// Preserve CIDRs, mapped addresses and zones exactly as the OS could return them.
type interfaceAddr string

func (a interfaceAddr) Network() string { return "ip" }
func (a interfaceAddr) String() string  { return string(a) }

func TestWildcardBindAggregatesInterfaces(t *testing.T) {
	source := interfaceSource{
		interfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Name: "eth0", Flags: net.FlagUp}, {Name: "tailscale0", Flags: net.FlagUp}}, nil
		},
		addrs: func(iface net.Interface) ([]net.Addr, error) {
			if iface.Name == "eth0" {
				return []net.Addr{interfaceAddr("192.168.1.2/24"), interfaceAddr("8.8.8.8/24"), interfaceAddr("2606:4700::1/64")}, nil
			}
			return []net.Addr{interfaceAddr("100.64.0.1/10"), interfaceAddr("fd7a:115c:a1e0::1/128")}, nil
		},
	}
	var logs bytes.Buffer
	warnWildcardBind(newLogger("json", &logs), []proxy.Forward{boundTestForward(&net.TCPAddr{IP: net.IPv6zero, Port: 8080}, 80)}, source)
	var record struct {
		Exposures, Listeners []string
	}
	if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	want := []string{"eth0=8.8.8.8", "eth0=2606:4700::1", "tailscale0=100.64.0.1", "tailscale0=fd7a:115c:a1e0::1"}
	if !reflect.DeepEqual(record.Exposures, want) || !reflect.DeepEqual(record.Listeners, []string{"8080:80"}) || strings.Count(logs.String(), "\n") != 1 {
		t.Fatalf("exposures=%v, want %v in one log line; logs=%s", record.Exposures, want, &logs)
	}
}

func TestWildcardBindInspectionFailures(t *testing.T) {
	for _, fail := range []string{"interfaces", "addrs", "malformed address"} {
		t.Run(fail, func(t *testing.T) {
			source := interfaceSource{
				interfaces: func() ([]net.Interface, error) {
					if fail == "interfaces" {
						return nil, errors.New("interface lookup failed")
					}
					return []net.Interface{{Name: "eth0", Flags: net.FlagUp}}, nil
				},
				addrs: func(net.Interface) ([]net.Addr, error) {
					if fail == "addrs" {
						return nil, errors.New("address lookup failed")
					}
					return []net.Addr{interfaceAddr("bad address")}, nil
				},
			}
			var logs bytes.Buffer
			warnWildcardBind(newLogger("json", &logs), []proxy.Forward{boundTestForward(&net.TCPAddr{IP: net.IPv4zero, Port: 4700}, 4700)}, source)
			if strings.Count(logs.String(), "\n") != 1 || !strings.Contains(logs.String(), `"inspection_errors"`) || !strings.Contains(logs.String(), "explicit LAN bind") {
				t.Fatalf("missing advisory inspection failure: %s", &logs)
			}
		})
	}
}

func TestListenerStartupBindWarnings(t *testing.T) {
	for _, tc := range []struct {
		bind string
		fail bool
		warn bool
	}{
		{"0.0.0.0", false, true},
		{"0.0.0.0", true, true},
		{"127.0.0.1", false, false},
		{"127.0.0.1", true, false},
	} {
		t.Run(fmt.Sprintf("%s/fail=%v", tc.bind, tc.fail), func(t *testing.T) {
			calls, addrCalls := 0, 0
			source := interfaceSource{
				interfaces: func() ([]net.Interface, error) {
					calls++
					if tc.fail {
						return nil, errors.New("interface lookup failed")
					}
					return []net.Interface{{Name: "tailscale0", Flags: net.FlagUp}}, nil
				},
				addrs: func(net.Interface) ([]net.Addr, error) {
					addrCalls++
					return []net.Addr{&net.IPNet{IP: net.ParseIP("100.64.0.1"), Mask: net.CIDRMask(10, 32)}}, nil
				},
			}
			var logs bytes.Buffer
			var forwards []proxy.Forward
			var wantListeners []string
			// Port zero asks the OS for unused ports, avoiding fixed-port conflicts.
			for _, p := range (mappings{{0, 4700}, {0, 80}, {0, 445}, {0, 4350}, {0, 4554}, {0, 4555}, {0, 4800}, {0, 4801}, {0, 4804}, {0, 4361}}) {
				l, err := net.Listen("tcp", net.JoinHostPort(tc.bind, "0"))
				if err != nil {
					t.Fatalf("startup with inspection failure=%v: err=%v", tc.fail, err)
				}
				defer l.Close()
				forwards = append(forwards, proxy.Forward{Listener: l, Port: p.device})
				wantListeners = append(wantListeners, fmt.Sprintf("%d:%d", l.Addr().(*net.TCPAddr).Port, p.device))
			}
			warnWildcardBind(newLogger("json", &logs), forwards, source)
			wantCalls := 0
			if tc.warn {
				wantCalls = 1
			}
			if calls != wantCalls || strings.Count(logs.String(), "\n") != wantCalls {
				t.Fatalf("enumerations=%d, log lines=%d, want %d for the whole startup; logs=%s", calls, strings.Count(logs.String(), "\n"), wantCalls, &logs)
			}
			wantAddrCalls := wantCalls
			if tc.fail {
				wantAddrCalls = 0
			}
			if addrCalls != wantAddrCalls {
				t.Fatalf("address lookups=%d, want %d", addrCalls, wantAddrCalls)
			}
			if tc.warn {
				var record struct {
					Level, Msg, Advice   string
					Listeners, Exposures []string
				}
				if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Fatal(err)
				}
				if record.Level != "WARN" || record.Msg != "wildcard_bind_warning" || !reflect.DeepEqual(record.Listeners, wantListeners) || !strings.Contains(record.Advice, "TCP listeners") {
					t.Fatalf("incomplete startup warning: %s", &logs)
				}
				if !tc.fail && !reflect.DeepEqual(record.Exposures, []string{"tailscale0=100.64.0.1"}) {
					t.Fatalf("exposures=%v, want one tailscale0=100.64.0.1", record.Exposures)
				}
			}
		})
	}
}

// Only Addr is used when inspecting already-opened TCP forwards.
type addressedListener struct {
	net.Listener
	addr *net.TCPAddr
}

func (l addressedListener) Addr() net.Addr { return l.addr }

func boundTestForward(addr *net.TCPAddr, port uint16) proxy.Forward {
	return proxy.Forward{Listener: addressedListener{addr: addr}, Port: port}
}

func TestWildcardBindMixedListeners(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		forwards             []proxy.Forward
		listeners, exposures []string
	}{
		{"empty", nil, nil, nil},
		{"IPv4 with explicit IPv6", []proxy.Forward{
			boundTestForward(&net.TCPAddr{IP: net.IPv4zero, Port: 4445}, 445),
			boundTestForward(&net.TCPAddr{IP: net.ParseIP("2606:4700::2"), Port: 8080}, 80),
		}, []string{"4445:445"}, []string{"eth0=8.8.8.8"}},
		{"IPv4 and IPv6 wildcards", []proxy.Forward{
			boundTestForward(&net.TCPAddr{IP: net.IPv4zero, Port: 4445}, 445),
			boundTestForward(&net.TCPAddr{IP: net.IPv6zero, Port: 8080}, 80),
			boundTestForward(&net.TCPAddr{IP: net.ParseIP("192.168.1.2"), Port: 4700}, 4700),
		}, []string{"4445:445", "8080:80"}, []string{"eth0=8.8.8.8", "eth0=2606:4700::1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			source := interfaceSource{
				interfaces: func() ([]net.Interface, error) {
					calls++
					return []net.Interface{{Name: "eth0", Flags: net.FlagUp}}, nil
				},
				addrs: func(net.Interface) ([]net.Addr, error) {
					return []net.Addr{interfaceAddr("8.8.8.8/24"), interfaceAddr("2606:4700::1/64")}, nil
				},
			}
			var logs bytes.Buffer
			warnWildcardBind(newLogger("json", &logs), tc.forwards, source)
			if len(tc.listeners) == 0 {
				if calls != 0 || logs.Len() != 0 {
					t.Fatalf("empty startup: enumerations=%d, logs=%s, want neither", calls, &logs)
				}
				return
			}
			var record struct{ Listeners, Exposures []string }
			if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !reflect.DeepEqual(record.Listeners, tc.listeners) || !reflect.DeepEqual(record.Exposures, tc.exposures) {
				t.Fatalf("enumerations=%d, listeners=%v, exposures=%v; want 1, %v, %v", calls, record.Listeners, record.Exposures, tc.listeners, tc.exposures)
			}
		})
	}
}
