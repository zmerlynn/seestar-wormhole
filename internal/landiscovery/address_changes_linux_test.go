//go:build linux

// These integration cases exercise address changes and floods from routed and
// rejected sources.
package landiscovery

import (
	"context"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Disposable namespace only. The Unix control socket joins Serve before
// reading its single-owner token count, avoiding a data race in the probe.
func TestLANAddressChangesServer(t *testing.T) {
	if os.Getenv("LAN_DISCOVERY_NETWORK_SERVER") != "1" {
		t.Skip("fixture only")
	}
	s, err := Listen(os.Getenv("LAN_DISCOVERY_BIND"), 0, Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := New("SYNTHETIC-SN", "Synthetic name", "S50")
	if err != nil {
		t.Fatal(err)
	}
	flood := os.Getenv("LAN_DISCOVERY_FLOOD_COUNT") != ""
	rejectedFlood := os.Getenv("LAN_DISCOVERY_REJECTED_FLOOD") == "1"
	var clock atomic.Int64
	if rejectedFlood {
		// Freeze inspection refill while processing the flood, then advance via
		// the control handshake. No timing/delivery assumptions set the budget.
		s.elapsed = func() time.Duration { return time.Duration(clock.Load()) }
	}
	if os.Getenv("LAN_DISCOVERY_EXHAUSTED_BUCKET") == "1" {
		// Freeze the injected refill epoch beyond this fixture's lifetime.
		m.tokens, m.last = 0, time.Hour
	}
	var inspections int
	s.addresses = func(index int) []netip.Prefix {
		inspections++
		if rejectedFlood && inspections == inspectionBurst+1 && m.tokens != ReplyBurst {
			t.Errorf("rejected scans spent reply tokens: got=%d want=%d", m.tokens, ReplyBurst)
		}
		return interfacePrefixes(netInterfaceSource{}, index, Options{})
	}
	var memoryBefore runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memoryBefore)
	ctrl, err := net.Listen("unix", "/evidence/control")
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	tcp, err := net.Listen("tcp4", "0.0.0.0:4700")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, m) }()
	defer func() { cancel(); s.Close() }()
	t.Logf("startup prefixes=%v", s.interfaces)
	if err := os.WriteFile("/evidence/probe-port", []byte(s.conn.LocalAddr().String()), 0600); err != nil {
		t.Fatal(err)
	}
	ctrl.(*net.UnixListener).SetDeadline(time.Now().Add(12 * time.Second))
	c, err := ctrl.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if rejectedFlood {
		clock.Store(int64(time.Second))
		if err := json.MarshalEncode(jsontext.NewEncoder(c), true); err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(30 * time.Second))
		var finished bool
		if err := json.UnmarshalRead(c, &finished, json.MatchCaseInsensitiveNames(true)); err != nil || !finished {
			t.Fatalf("recovery handshake: finished=%v err=%v", finished, err)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if flood {
		var memoryAfter runtime.MemStats
		runtime.ReadMemStats(&memoryAfter)
		allocated := memoryAfter.TotalAlloc - memoryBefore.TotalAlloc
		t.Logf("interface inspections=%d allocated-bytes=%d", inspections, allocated)
		want := 0
		if rejectedFlood {
			want = inspectionBurst + 1 // burst plus the single refilled recovery
		}
		if inspections != want {
			t.Errorf("flood interface inspections=%d want=%d", inspections, want)
		}
		if allocated > 16<<20 {
			t.Errorf("flood allocated %d bytes, bound %d", allocated, 16<<20)
		}
	}
	t.Logf("remaining tokens=%d", m.tokens)
	if err := json.MarshalEncode(jsontext.NewEncoder(c), m.tokens); err != nil {
		t.Fatal(err)
	}
}

type lanAddressChangeCase struct {
	Source, Destination, Reply string
	Reject                     bool
}

func TestLANAddressChangesClient(t *testing.T) {
	if os.Getenv("LAN_DISCOVERY_NETWORK_CLIENT") != "1" {
		t.Skip("fixture only")
	}
	var cases []lanAddressChangeCase
	if err := json.Unmarshal([]byte(os.Getenv("LAN_DISCOVERY_CASES")), &cases, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("/evidence/probe-port")
	if err != nil {
		t.Fatal(err)
	}
	addr, err := net.ResolveUDPAddr("udp4", string(b))
	if err != nil {
		t.Fatal(err)
	}
	var ctrl net.Conn
	if count, _ := strconv.Atoi(os.Getenv("LAN_DISCOVERY_FLOOD_COUNT")); count > 0 {
		source := "192.168.50.3"
		if s := os.Getenv("LAN_DISCOVERY_FLOOD_SOURCE"); s != "" {
			source = s
		}
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(source)})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		addr.IP = net.ParseIP("192.168.50.2")
		payload := []byte("x" + strings.Repeat(" ", MaxDatagram-1))
		if os.Getenv("LAN_DISCOVERY_EXHAUSTED_BUCKET") == "1" || os.Getenv("LAN_DISCOVERY_REJECTED_FLOOD") == "1" {
			payload = []byte(`{"id":7,"method":"scan_iscope"}`)
		}
		// Pace batches to allow the socket to drain.
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; i < count; i++ {
			if _, err := c.WriteToUDP(payload, addr); err != nil {
				t.Fatal(err)
			}
			if i%16 == 15 {
				<-ticker.C
			}
		}
		c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if n, from, err := c.ReadFromUDP(make([]byte, MaxDatagram+1)); err == nil {
			t.Fatalf("flood reply from=%v bytes=%d", from, n)
		} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
			t.Fatal(err)
		}
		t.Logf("sent=%d bytes-per-datagram=%d", count, len(payload))
		if os.Getenv("LAN_DISCOVERY_REJECTED_FLOOD") == "1" {
			// A legitimate scan at the exhausted inspection epoch also fails
			// closed. The same source must recover after the control refill.
			lan, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("192.168.50.3")})
			if err != nil {
				t.Fatal(err)
			}
			defer lan.Close()
			if _, err := lan.WriteToUDP(payload, addr); err != nil {
				t.Fatal(err)
			}
			lan.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			if _, _, err := lan.ReadFromUDP(make([]byte, MaxDatagram+1)); err == nil {
				t.Fatal("exhausted inspection budget answered on-LAN scan")
			} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
				t.Fatal(err)
			}
			ctrl, err = net.DialTimeout("unix", "/evidence/control", 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer ctrl.Close()
			var refilled bool
			if err := json.UnmarshalRead(ctrl, &refilled, json.MatchCaseInsensitiveNames(true)); err != nil || !refilled {
				t.Fatalf("refill handshake: refilled=%v err=%v", refilled, err)
			}
		}
	}
	for i, tc := range cases {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(tc.Source)})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := c.SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		var optErr error
		if err := raw.Control(func(fd uintptr) { optErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1) }); err != nil || optErr != nil {
			t.Fatalf("broadcast option: %v %v", err, optErr)
		}
		addr.IP = net.ParseIP(tc.Destination)
		_, err = ipv4.NewPacketConn(c).WriteTo([]byte(`{"id":7,"method":"scan_iscope"}`), &ipv4.ControlMessage{Src: net.ParseIP(tc.Source)}, addr)
		if err != nil {
			t.Fatal(err)
		}
		bound := 30 * time.Second
		if tc.Reject {
			bound = 100 * time.Millisecond
		}
		c.SetReadDeadline(time.Now().Add(bound))
		response := make([]byte, 4097)
		n, from, err := c.ReadFromUDP(response)
		c.Close()
		if tc.Reject {
			if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
				t.Errorf("case %d rejected source=%s destination=%s got from=%v n=%d err=%v", i, tc.Source, tc.Destination, from, n, err)
			} else {
				t.Logf("rejected source=%s destination=%s", tc.Source, tc.Destination)
			}
		} else {
			if err != nil {
				t.Errorf("case %d source=%s destination=%s missing reply: %v", i, tc.Source, tc.Destination, err)
				continue
			}
			if from.IP.String() != tc.Reply {
				t.Errorf("reply source=%s want=%s", from.IP, tc.Reply)
			}
			dialer := net.Dialer{Timeout: 30 * time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(tc.Source)}}
			conn, err := dialer.DialContext(t.Context(), "tcp4", net.JoinHostPort(from.IP.String(), "4700"))
			if err != nil {
				t.Errorf("phone cannot dial reply source %v: %v", from, err)
			} else {
				conn.Close()
			}
			t.Logf("source=%s destination=%s reply source=%s bytes=%d TCP dial=%v", tc.Source, tc.Destination, from, n, err)
		}
	}
	if ctrl == nil {
		ctrl, err = net.DialTimeout("unix", "/evidence/control", 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer ctrl.Close()
	} else if err := json.MarshalEncode(jsontext.NewEncoder(ctrl), true); err != nil {
		t.Fatal(err)
	}
	var tokens int
	if err := json.UnmarshalRead(ctrl, &tokens, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	want, err := strconv.Atoi(os.Getenv("LAN_DISCOVERY_TOKENS"))
	if err != nil {
		t.Fatal(err)
	}
	if tokens != want {
		t.Errorf("remaining tokens=%d want=%d", tokens, want)
	} else {
		t.Logf("remaining tokens=%d want=%d", tokens, want)
	}
}
