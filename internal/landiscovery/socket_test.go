//go:build unix

package landiscovery

import (
	"bytes"
	"context"
	"github.com/go-json-experiment/json"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestLANBindValidation(t *testing.T) {
	for _, bind := range []string{"127.0.0.1", "localhost", "::", "224.0.0.1", "255.255.255.255", "192.0.2.45"} {
		if _, err := Listen(bind, 0); err == nil {
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
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
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
	case <-time.After(3 * time.Second):
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

// The isolated container has only loopback. Select it explicitly in ancillary
// data so even limited broadcast is delivered on this disposable test socket.
// No packet leaves the network namespace or targets a device/service port.
func TestUDPBroadcastReceipt(t *testing.T) {
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		lo, err = net.InterfaceByName("lo0")
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range []net.IP{net.IPv4(127, 255, 255, 255), net.IPv4bcast} {
		t.Run(destination.String(), func(t *testing.T) {
			s, err := listen(0, map[int][]netip.Prefix{lo.Index: {netip.MustParsePrefix("127.0.0.1/8")}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			m, _, _ := New("SN", "name", "S50")
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- s.Serve(ctx, m) }()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			raw, err := sender.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			var optionErr error
			if err := raw.Control(func(fd uintptr) { optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BROADCAST, 1) }); err != nil {
				t.Fatal(err)
			}
			if optionErr != nil {
				t.Fatal(optionErr)
			}
			dst := &net.UDPAddr{IP: destination, Port: s.addr().Port}
			if _, err := ipv4.NewPacketConn(sender).WriteTo([]byte(scan), &ipv4.ControlMessage{Src: net.IPv4(127, 0, 0, 1), IfIndex: lo.Index}, dst); err != nil {
				t.Fatal(err)
			}
			sender.SetReadDeadline(time.Now().Add(3 * time.Second))
			b := make([]byte, MaxDatagram)
			n, from, err := sender.ReadFromUDP(b)
			if err != nil || n == 0 || from.Port != dst.Port || !from.IP.Equal(net.IPv4(127, 0, 0, 1)) {
				t.Fatalf("broadcast request, unicast reply source=%v n=%d err=%v", from, n, err)
			}
		})
	}
}

func loopbackSources(t *testing.T) map[int][]netip.Prefix {
	t.Helper()
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		lo, err = net.InterfaceByName("lo0")
	}
	if err != nil {
		t.Fatal(err)
	}
	return map[int][]netip.Prefix{lo.Index: {netip.MustParsePrefix("127.0.0.1/8")}}
}
