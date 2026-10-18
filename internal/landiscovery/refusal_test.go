package landiscovery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"golang.org/x/net/ipv4"
)

func TestDiscoveryRefusalSummaries(t *testing.T) {
	for _, tc := range []struct {
		name, wire, rule string
		change           func(*Socket, *Machine, **ipv4.ControlMessage, *net.Addr)
		code             int
	}{
		{name: "malformed", wire: `{`, rule: "malformed_scan"},
		{name: "duplicate", wire: `{"id":7,"id":8}`, rule: "malformed_scan"},
		{name: "null", wire: `null`, rule: "malformed_scan"},
		{name: "array", wire: `[]`, rule: "malformed_scan"},
		{name: "oversized scan", wire: strings.Repeat("x", MaxDatagram+1), rule: "oversized_scan"},
		{name: "oversized reply", wire: `{"id":"` + strings.Repeat("x", 4000) + `","method":"scan_iscope"}`, rule: "oversized_reply"},
		{name: "unknown method", wire: `{"id":7,"method":"secret-method"}`, rule: "unknown_method"},
		{name: "missing id", wire: `{"method":"scan_iscope"}`, rule: "missing_id", code: 200},
		{name: "missing method", wire: `{"id":7}`, rule: "invalid_method", code: 100},
		{name: "null method", wire: `{"id":7,"method":null}`, rule: "invalid_method", code: 100},
		{name: "nonstring method", wire: `{"id":7,"method":42}`, rule: "invalid_method", code: 100},
		{name: "reply rate", rule: "reply_rate", change: func(_ *Socket, m *Machine, _ **ipv4.ControlMessage, _ *net.Addr) { m.tokens = 0 }},
		{name: "inspection rate", rule: "inspection_rate", change: func(s *Socket, _ *Machine, _ **ipv4.ControlMessage, _ *net.Addr) { s.inspectionUsed = inspectionBurst }},
		{name: "unselected interface", rule: "interface_unavailable", change: func(_ *Socket, _ *Machine, cm **ipv4.ControlMessage, _ *net.Addr) { (*cm).IfIndex = 2 }},
		{name: "removed inactive or failed inspection", rule: "interface_unavailable", change: func(s *Socket, _ *Machine, _ **ipv4.ControlMessage, _ *net.Addr) {
			s.addresses = func(int) []netip.Prefix { return nil }
		}},
		{name: "replaced host IP", rule: "interface_unavailable", change: func(s *Socket, _ *Machine, _ **ipv4.ControlMessage, _ *net.Addr) {
			s.addresses = func(int) []netip.Prefix { return []netip.Prefix{netip.MustParsePrefix("192.0.2.3/24")} }
		}},
		{name: "routed source", rule: "wrong_source", change: func(_ *Socket, _ *Machine, _ **ipv4.ControlMessage, src *net.Addr) {
			*src = &net.UDPAddr{IP: net.ParseIP("198.51.100.1"), Port: 1234}
		}},
		{name: "network source", rule: "wrong_source", change: func(_ *Socket, _ *Machine, _ **ipv4.ControlMessage, src *net.Addr) {
			*src = &net.UDPAddr{IP: net.ParseIP("192.0.2.0"), Port: 1234}
		}},
		{name: "broadcast source", rule: "wrong_source", change: func(_ *Socket, _ *Machine, _ **ipv4.ControlMessage, src *net.Addr) {
			*src = &net.UDPAddr{IP: net.ParseIP("192.0.2.255"), Port: 1234}
		}},
		{name: "unspecified source", rule: "wrong_source", change: func(_ *Socket, _ *Machine, _ **ipv4.ControlMessage, src *net.Addr) {
			*src = &net.UDPAddr{IP: net.IPv4zero, Port: 1234}
		}},
		{name: "multicast source", rule: "wrong_source", change: func(_ *Socket, _ *Machine, _ **ipv4.ControlMessage, src *net.Addr) {
			*src = &net.UDPAddr{IP: net.ParseIP("224.0.0.1"), Port: 1234}
		}},
		{name: "wrong destination", rule: "wrong_source", change: func(_ *Socket, _ *Machine, cm **ipv4.ControlMessage, _ *net.Addr) {
			(*cm).Dst = net.ParseIP("192.0.2.99")
		}},
		{name: "ambiguous alias", rule: "ambiguous_alias", change: func(s *Socket, _ *Machine, cm **ipv4.ControlMessage, _ *net.Addr) {
			s.interfaces[1] = append(s.interfaces[1], netip.MustParsePrefix("192.0.2.3/24"))
			(*cm).Dst = net.IPv4bcast
		}},
		{name: "missing metadata", rule: "packet_metadata", change: func(_ *Socket, _ *Machine, cm **ipv4.ControlMessage, _ *net.Addr) { *cm = nil }},
		{name: "non UDP source", rule: "packet_metadata", change: func(_ *Socket, _ *Machine, _ **ipv4.ControlMessage, src *net.Addr) { *src = &net.TCPAddr{} }},
		{name: "zero source port", rule: "packet_metadata", change: func(_ *Socket, _ *Machine, _ **ipv4.ControlMessage, src *net.Addr) { (*src).(*net.UDPAddr).Port = 0 }},
		{name: "invalid source", rule: "packet_metadata", change: func(_ *Socket, _ *Machine, _ **ipv4.ControlMessage, src *net.Addr) { (*src).(*net.UDPAddr).IP = nil }},
		{name: "invalid destination", rule: "packet_metadata", change: func(_ *Socket, _ *Machine, cm **ipv4.ControlMessage, _ *net.Addr) { (*cm).Dst = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			s := &Socket{Logger: slog.New(slog.NewJSONHandler(&logs, nil)), interfaces: map[int][]netip.Prefix{1: {netip.MustParsePrefix("192.0.2.1/24")}}}
			s.addresses = func(i int) []netip.Prefix { return s.interfaces[i] }
			m, _, _ := New("secret-serial", "secret-name", "S50")
			cm := &ipv4.ControlMessage{IfIndex: 1, Dst: net.ParseIP("192.0.2.1")}
			var source net.Addr = &net.UDPAddr{IP: net.ParseIP("192.0.2.2"), Port: 1234}
			if tc.change != nil {
				tc.change(s, m, &cm, &source)
			}
			wire := tc.wire
			if wire == "" {
				wire = scan
			}
			for range 3 {
				out, _ := s.received(m, []byte(wire), cm, source, 0)
				if tc.code == 0 {
					if out != nil {
						t.Fatalf("discard replied: %s", out)
					}
				} else {
					var r reply
					if err := json.Unmarshal(out, &r); err != nil {
						t.Fatal(err)
					}
					id := "7"
					if tc.code == 200 {
						id = "null"
					}
					if r.Code != tc.code || string(r.ID) != id || r.Result != nil {
						t.Fatalf("dispatcher reply=%s", out)
					}
				}
			}
			var total uint64
			for rule, count := range m.refusals {
				total += count
				if refusalRules[rule] == tc.rule && count != 3 {
					t.Fatalf("counter=%d want 3", count)
				}
			}
			if total != 3 {
				t.Fatalf("total=%d want 3", total)
			}
			s.summarize(m, refusalInterval-1, false)
			if logs.Len() != 0 {
				t.Fatal("WARN before interval")
			}
			s.summarize(m, refusalInterval, false)
			var record struct {
				Level, Msg, Rule string
				Count            uint64
			}
			if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatal(err)
			}
			if record.Level != "WARN" || record.Msg != "lan_discovery_refused" || record.Rule != tc.rule || record.Count != 3 {
				t.Fatalf("summary=%s", &logs)
			}
			if strings.Contains(logs.String(), "secret") {
				t.Fatal("peer data in summary")
			}
			logs.Reset()
			s.summarize(m, 2*refusalInterval, false)
			if logs.Len() != 0 {
				t.Fatal("repeated empty summary")
			}
			m.refuse(refusalMalformed)
			s.summarize(m, 2*refusalInterval, true)
			if !strings.Contains(logs.String(), `"count":1`) {
				t.Fatal("missing final pending count")
			}
		})
	}
}

// A narrow socket seam injects local write failures; no packet or live listener
// is needed to prove the caller receives the error and its owner logs the rule.
type failedPacket struct {
	deadline, write error
	short           bool
}

func (*failedPacket) ReadFrom([]byte) (int, *ipv4.ControlMessage, net.Addr, error) {
	return 0, nil, nil, io.EOF
}
func (*failedPacket) SetReadDeadline(time.Time) error    { return nil }
func (p *failedPacket) SetWriteDeadline(time.Time) error { return p.deadline }
func (p *failedPacket) WriteTo(b []byte, _ *ipv4.ControlMessage, _ net.Addr) (int, error) {
	if p.short {
		return len(b) - 1, nil
	}
	return len(b), p.write
}

type receivePacket struct {
	failedPacket
	read func([]byte) (int, *ipv4.ControlMessage, net.Addr, error)
}

func (p *receivePacket) ReadFrom(b []byte) (int, *ipv4.ControlMessage, net.Addr, error) {
	return p.read(b)
}

func TestServeRefusalFlush(t *testing.T) {
	for _, name := range []string{"interval", "shutdown"} {
		t.Run(name, func(t *testing.T) {
			conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var now time.Duration
			var logs bytes.Buffer
			checkSummary := func() {
				t.Helper()
				var record struct {
					Level, Msg, Rule string
					Count            uint64
				}
				if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Fatalf("want exactly one refusal summary, got %q: %v", &logs, err)
				}
				if record.Level != "WARN" || record.Msg != "lan_discovery_refused" || record.Rule != "malformed_scan" || record.Count != 1 {
					t.Fatalf("want malformed_scan WARN with count 1, got %s", &logs)
				}
			}
			reads := 0
			packet := &receivePacket{}
			packet.read = func(b []byte) (int, *ipv4.ControlMessage, net.Addr, error) {
				reads++
				if reads == 1 {
					return copy(b, "{"), &ipv4.ControlMessage{Dst: net.IPv4(192, 0, 2, 1), IfIndex: 1}, &net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 1234}, nil
				}
				if reads == 2 {
					if logs.Len() != 0 {
						t.Errorf("want no WARN before interval or shutdown, got %s", &logs)
					}
					if name == "interval" {
						now = refusalInterval
						return 0, nil, nil, &net.DNSError{IsTimeout: true}
					}
				}
				if name == "interval" {
					// Check before cancellation so the final flush cannot hide a lost interval flush.
					checkSummary()
				}
				cancel()
				return 0, nil, nil, net.ErrClosed
			}
			s := &Socket{conn: conn, packet: packet, elapsed: func() time.Duration { return now }, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			m, _, err := New("synthetic", "synthetic", "S50")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Serve(ctx, m); err != nil {
				t.Fatalf("Serve cancellation returned %v, want nil", err)
			}
			checkSummary()
		})
	}
}

func TestDiscoveryDeliveryRefusals(t *testing.T) {
	private := errors.New("secret-socket-error")
	for _, tc := range []struct {
		name, rule string
		packet     failedPacket
	}{
		{"deadline", "reply_deadline_failed", failedPacket{deadline: private}},
		{"write", "reply_write_failed", failedPacket{write: private}},
		{"short", "reply_short_write", failedPacket{short: true}},
		{"success", "", failedPacket{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			s := &Socket{packet: &tc.packet, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			err := s.deliver([]byte("reply"), netip.MustParseAddr("192.0.2.1"), 1, &net.UDPAddr{})
			if tc.rule == "" {
				if err != nil || logs.Len() != 0 {
					t.Fatalf("success: %v %s", err, &logs)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.rule) || !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), `"rule":"`+tc.rule+`"`) {
				t.Fatalf("local failure: %v logs=%s", err, &logs)
			}
			if strings.Contains(logs.String(), "secret") {
				t.Fatal("dependency error leaked")
			}
		})
	}
}
