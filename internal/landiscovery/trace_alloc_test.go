//go:build !race

// Standard library JSON uses sync.Pool, which randomly discards entries under
// race instrumentation. Compare allocation counts with non-race main builds.
package landiscovery

import (
	"net/netip"
	"testing"
	"time"
)

func TestTraceOffLANAllocationBaseline(t *testing.T) {
	m, _, err := New("synthetic", "synthetic", "S50")
	if err != nil {
		t.Fatal(err)
	}
	prefix := netip.MustParsePrefix("192.0.2.1/24")
	prefixes := []netip.Prefix{prefix}
	s := &Socket{interfaces: map[int][]netip.Prefix{1: prefixes}, addresses: func(int) []netip.Prefix { return prefixes }}
	requester := netip.MustParseAddr("192.0.2.7")
	var now time.Duration
	got := testing.AllocsPerRun(1000, func() {
		// Stack-sized receive buffers expose any recorder-induced escape. Advance
		// owner time to refill both admission budgets for every valid scan.
		var b [MaxDatagram]byte
		n := copy(b[:], `{"id":7,"method":"scan_iscope"}`)
		now += time.Second
		if response, _ := s.response(m, b[:n], now, 1, requester, prefix.Addr()); response == nil {
			panic("scan rejected")
		}
	})
	t.Logf("trace absent LAN response allocations=%v", got)
	// Raw ids and separately decoded method types add four allocations to
	// the original integer-only dispatcher baseline. Trace remains absent.
	if got != 12 {
		t.Fatalf("allocations=%v want dispatcher baseline 12", got)
	}
}
