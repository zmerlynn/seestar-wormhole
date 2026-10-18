package landiscovery

import (
	"net/netip"
	"testing"
)

func TestOnLinkBoundaries(t *testing.T) {
	for _, tc := range []struct {
		prefix, source string
		want           bool
	}{
		{"192.168.50.2/24", "192.168.50.1", true},
		{"192.168.50.2/24", "192.168.50.254", true},
		{"192.168.50.2/24", "192.168.50.0", false},
		{"192.168.50.2/24", "192.168.50.255", false},
		{"192.168.50.2/24", "198.51.100.9", false},
		{"192.168.50.2/24", "172.16.3.3", false},
		{"192.168.50.2/24", "192.168.49.255", false},
		{"192.168.50.2/24", "192.168.51.0", false},
		{"192.168.50.2/24", "0.0.0.0", false},
		{"192.168.50.2/24", "224.0.0.1", false},
		{"192.168.50.2/24", "255.255.255.255", false},
		{"192.168.50.2/24", "::1", false},
		{"192.168.50.2/31", "192.168.50.2", true},
		{"192.168.50.2/31", "192.168.50.3", true},
		{"192.168.50.2/32", "192.168.50.2", true},
		{"192.168.50.2/32", "192.168.50.3", false},
	} {
		if got := onLink(netip.MustParsePrefix(tc.prefix), netip.MustParseAddr(tc.source)); got != tc.want {
			t.Errorf("prefix=%s source=%s admitted=%v, want %v", tc.prefix, tc.source, got, tc.want)
		}
	}
}

func TestReplySourceDestination(t *testing.T) {
	prefixes := []netip.Prefix{netip.MustParsePrefix("192.168.50.2/24"), netip.MustParsePrefix("172.16.3.2/24")}
	for _, tc := range []struct{ source, destination, want string }{
		{"192.168.50.3", "192.168.50.2", "192.168.50.2"},
		{"192.168.50.3", "192.168.50.255", "192.168.50.2"},
		{"192.168.50.3", "255.255.255.255", "192.168.50.2"},
		{"172.16.3.3", "172.16.3.2", "172.16.3.2"},
		{"172.16.3.3", "172.16.3.255", "172.16.3.2"},
		{"172.16.3.3", "255.255.255.255", "172.16.3.2"},
		{"172.16.3.3", "192.168.50.2", ""},
		{"172.16.3.3", "192.168.50.255", ""},
		{"172.16.3.3", "172.16.3.99", ""},
		{"198.51.100.9", "172.16.3.2", ""},
	} {
		got := replySource(prefixes, netip.MustParseAddr(tc.source), netip.MustParseAddr(tc.destination))
		var want netip.Addr
		if tc.want != "" {
			want = netip.MustParseAddr(tc.want)
		}
		if got != want {
			t.Errorf("source=%s destination=%s got=%s want=%s", tc.source, tc.destination, got, want)
		}
	}
	aliases := []netip.Prefix{prefixes[0], netip.MustParsePrefix("192.168.50.4/24")}
	if got := replySource(aliases, netip.MustParseAddr("192.168.50.3"), netip.MustParseAddr("255.255.255.255")); got.IsValid() {
		t.Errorf("ambiguous limited broadcast chose %s", got)
	}
	if got := replySource(aliases, netip.MustParseAddr("192.168.50.3"), netip.MustParseAddr("192.168.50.4")); got != aliases[1].Addr() {
		t.Errorf("unicast alias got %s want %s", got, aliases[1].Addr())
	}
}
