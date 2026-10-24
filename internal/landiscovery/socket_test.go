//go:build unix || windows

package landiscovery

import (
	"bytes"
	"context"
	"github.com/go-json-experiment/json"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestLANBindValidation(t *testing.T) {
	for _, bind := range []string{"127.0.0.1", "localhost", "::", "224.0.0.1", "255.255.255.255", "192.0.2.45"} {
		if _, err := Listen(bind, 0, Options{}); err == nil {
			t.Fatalf("accepted non-LAN/unassigned bind %s", bind)
		}
	}
}

func TestUDPUnicastAndShutdown(t *testing.T) {
	// A private socket seam permits loopback while public Listen requires LAN.
	s, err := listen(0, loopbackSources(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, _, _ := New("SN", "name", "S50")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, m) }()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	other, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, bad := range [][]byte{[]byte(`{`), append([]byte(`{"id":99,"method":"scan_iscope"}`), bytes.Repeat([]byte(" "), MaxDatagram)...)} {
		if _, err := c.WriteToUDP(bad, s.addr()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.WriteToUDP([]byte(scan), s.addr()); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	b := make([]byte, MaxDatagram+1)
	n, from, err := c.ReadFromUDP(b)
	if err != nil || n == 0 || from.Port != s.addr().Port || !from.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("reply source %v, bytes %d, err %v", from, n, err)
	}
	var response struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(b[:n], &response, json.MatchCaseInsensitiveNames(true)); err != nil || response.ID != 7 {
		t.Fatalf("replied to malformed/oversize scan: id=%d err=%v", response.ID, err)
	}
	other.SetReadDeadline(time.Now())
	if _, _, err := other.ReadFromUDP(b); err == nil {
		t.Fatal("reply was broadcast to another socket")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("responder did not join on cancellation")
	}
}

func (s *Socket) addr() *net.UDPAddr {
	addr := *s.conn.LocalAddr().(*net.UDPAddr)
	addr.IP = net.IPv4(127, 0, 0, 1)
	for _, prefixes := range s.interfaces {
		if len(prefixes) > 0 {
			addr.IP = net.IP(prefixes[0].Addr().AsSlice())
			break
		}
	}
	return &addr
}

func loopbackSources(t *testing.T) map[int][]netip.Prefix {
	t.Helper()
	// Enumerate the owner of the assigned loopback address: Windows does not
	// use Unix interface names and does not assign Linux's 127.0.0.2 alias.
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			t.Fatal(err)
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err == nil && prefix.Addr() == netip.MustParseAddr("127.0.0.1") {
				return map[int][]netip.Prefix{iface.Index: {prefix}}
			}
		}
	}
	t.Fatal("127.0.0.1 has no interface")
	return nil
}
