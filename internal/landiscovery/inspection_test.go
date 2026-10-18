package landiscovery

import (
	"net/netip"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRejectedScansSkipInterfaceInspection(t *testing.T) {
	// Literal payload and count exercise bounded inspection under a flood. Invoke
	// the same handler as Serve so UDP losses cannot weaken the work assertion.
	for _, tc := range []struct {
		name, payload string
		tokens        int
	}{
		{"malformed flood", "x" + strings.Repeat(" ", MaxDatagram-1), ReplyBurst},
		{"wrong method", `{"id":7,"method":"get_device_state"}`, ReplyBurst},
		{"exhausted bucket", `{"id":7,"method":"scan_iscope"}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := netip.MustParsePrefix("192.168.50.2/24")
			requester := netip.MustParseAddr("192.168.50.3")
			var inspections int
			s := &Socket{interfaces: map[int][]netip.Prefix{3: {prefix}}, addresses: func(int) []netip.Prefix {
				inspections++
				return []netip.Prefix{prefix}
			}}
			m, _, err := New("SYNTHETIC-SN", "Synthetic name", "S50")
			if err != nil {
				t.Fatal(err)
			}
			m.tokens = tc.tokens
			payload := []byte(tc.payload)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			for i := 0; i < 5000; i++ {
				if reply, _ := s.response(m, payload, 0, 3, requester, prefix.Addr()); reply != nil {
					t.Fatalf("rejected scan %d produced a reply", i)
				}
			}
			runtime.ReadMemStats(&after)
			if inspections != 0 {
				t.Errorf("interface inspections=%d want=0", inspections)
			}
			if m.tokens != tc.tokens {
				t.Errorf("remaining tokens=%d want=%d", m.tokens, tc.tokens)
			}
			allocated := after.TotalAlloc - before.TotalAlloc
			// Includes standard JSON decoding and runtime overhead. The live
			// 64-interface fixture previously allocated about 1.2 GB.
			if allocated > 16<<20 {
				t.Errorf("allocated %d bytes, bound %d", allocated, 16<<20)
			}
			t.Logf("5000 scans: inspections=%d allocated-bytes=%d", inspections, allocated)
			// A later refill must reach live inspection and produce a reply.
			if reply, local := s.response(m, []byte(`{"id":7,"method":"scan_iscope"}`), time.Second, 3, requester, prefix.Addr()); reply == nil || local != prefix.Addr() || inspections != 1 {
				t.Errorf("eligible scan: reply bytes=%d source=%v inspections=%d", len(reply), local, inspections)
			}
		})
	}
}

func TestRejectedSourcesBoundInterfaceInspection(t *testing.T) {
	// A valid literal, routed source, and flood count exercise rejected-source
	// production socket fixture. Also exercise a formerly on-link source after
	// narrowing the live prefix; a startup prefix must not authorize a reply.
	for _, tc := range []struct{ name, source, current string }{
		{"routed", "198.51.100.9", "192.168.50.2/24"},
		{"narrowed", "192.168.50.100", "192.168.50.2/32"},
		{"removed", "192.168.50.3", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := netip.MustParsePrefix("192.168.50.2/24")
			var current []netip.Prefix
			if tc.current != "" {
				current = []netip.Prefix{netip.MustParsePrefix(tc.current)}
			}
			inspections := 0
			s := &Socket{interfaces: map[int][]netip.Prefix{3: {prefix}}, addresses: func(int) []netip.Prefix {
				inspections++
				return current
			}}
			m, _, err := New("SYNTHETIC-SN", "Synthetic name", "S50")
			if err != nil {
				t.Fatal(err)
			}
			payload := []byte(`{"id":7,"method":"scan_iscope"}`)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			for i := 0; i < 5000; i++ {
				if reply, _ := s.response(m, payload, 0, 3, netip.MustParseAddr(tc.source), prefix.Addr()); reply != nil {
					t.Fatalf("rejected source scan %d produced a reply", i)
				}
			}
			runtime.ReadMemStats(&after)
			if inspections != 4 || m.tokens != ReplyBurst {
				t.Errorf("5000 rejected scans: inspections=%d want=4 reply tokens=%d want=%d", inspections, m.tokens, ReplyBurst)
			}
			allocated := after.TotalAlloc - before.TotalAlloc
			if allocated > 16<<20 {
				t.Errorf("allocated %d bytes, bound %d", allocated, 16<<20)
			}
			t.Logf("5000 scans: inspections=%d allocated-bytes=%d", inspections, allocated)
			current = []netip.Prefix{prefix}
			requester := netip.MustParseAddr("192.168.50.3")
			for _, now := range []time.Duration{0, -time.Second, time.Second - 1} {
				if reply, _ := s.response(m, payload, now, 3, requester, prefix.Addr()); reply != nil || inspections != 4 || m.tokens != ReplyBurst {
					t.Errorf("exhausted at %s: reply bytes=%d inspections=%d tokens=%d", now, len(reply), inspections, m.tokens)
				}
			}
			// Exactly one inspection refills, and it must read the restored live
			// prefix before replying. A second scan at this instant is dropped.
			if reply, local := s.response(m, payload, time.Second, 3, requester, prefix.Addr()); reply == nil || local != prefix.Addr() || inspections != 5 || m.tokens != ReplyBurst-1 {
				t.Errorf("refilled: reply bytes=%d source=%v inspections=%d tokens=%d", len(reply), local, inspections, m.tokens)
			}
			if reply, _ := s.response(m, payload, time.Second, 3, requester, prefix.Addr()); reply != nil || inspections != 5 {
				t.Errorf("spent refill: reply bytes=%d inspections=%d", len(reply), inspections)
			}
		})
	}
}
