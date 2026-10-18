//go:build linux

package landiscovery

import (
	"context"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// Linux loopback owns 127/8 without configuring aliases; this gives a distinct
// proxy source to catch accidentally leaving route-selected source addressing.
func TestUDPReplySourceAndInterfaceFilter(t *testing.T) {
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	for _, filter := range []bool{false, true} {
		index := lo.Index
		if filter {
			index++
		}
		s, err := listen(0, map[int][]netip.Prefix{index: {netip.MustParsePrefix("127.0.0.2/8")}})
		if err != nil {
			t.Fatal(err)
		}
		m, _, _ := New("SN", "name", "S50")
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- s.Serve(ctx, m) }()
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			cancel()
			s.Close()
			<-done
			t.Fatal(err)
		}
		if _, err := c.WriteToUDP([]byte(scan), s.addr()); err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		_, from, err := c.ReadFromUDP(make([]byte, MaxDatagram))
		if filter {
			if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
				t.Errorf("wrong-interface request answered: %v, %v", from, err)
			}
		} else if err != nil || !from.IP.Equal(net.IPv4(127, 0, 0, 2)) {
			t.Errorf("reply source %v err %v, want 127.0.0.2", from, err)
		}
		c.Close()
		cancel()
		s.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

// Use a synthetic /24 on loopback to exercise admission without routing to a
// device. Linux permits all these source addresses without configuring aliases.
func TestUDPOnLinkAdmissionBeforeBudget(t *testing.T) {
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	s, err := listen(0, map[int][]netip.Prefix{lo.Index: {netip.MustParsePrefix("127.0.0.2/24")}})
	if err != nil {
		t.Fatal(err)
	}
	m, _, _ := New("SN", "name", "S50")
	// Refill only inspection work after the rejected scans. Freeze reply refill
	// beyond the fixture so it cannot hide accidental spending by admission.
	m.last = time.Hour
	var clock atomic.Int64
	s.elapsed = func() time.Duration { return time.Duration(clock.Load()) }
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, m) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for _, source := range []string{"127.1.0.9", "127.0.0.0", "127.0.0.255"} {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(source)})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < ReplyBurst; i++ {
			if _, err := c.WriteToUDP([]byte(scan), s.addr()); err != nil {
				t.Fatal(err)
			}
		}
		c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, from, err := c.ReadFromUDP(make([]byte, MaxDatagram))
		c.Close()
		if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
			t.Errorf("rejected source %s received reply from %v: %v", source, from, err)
		}
	}
	clock.Store(int64(4 * time.Second))
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < ReplyBurst; i++ {
		if _, err := c.WriteToUDP([]byte(scan), s.addr()); err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, _, err := c.ReadFromUDP(make([]byte, MaxDatagram)); err != nil {
			t.Fatalf("on-link burst reply %d missing after rejected scans: %v", i, err)
		}
	}
}

func TestUDPTwoSubnetsOnOneInterface(t *testing.T) {
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	// Both proxy addresses belong to one ingress interface, on separate /24s.
	s, err := listen(0, map[int][]netip.Prefix{lo.Index: {
		netip.MustParsePrefix("127.0.0.2/24"), netip.MustParsePrefix("127.1.0.2/24"),
	}})
	if err != nil {
		t.Fatal(err)
	}
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
	for _, subnet := range []struct{ phone, proxy string }{
		{"127.0.0.3", "127.0.0.2"}, {"127.1.0.3", "127.1.0.2"},
	} {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(subnet.phone)})
		if err != nil {
			t.Fatal(err)
		}
		dst := s.addr()
		dst.IP = net.ParseIP(subnet.proxy)
		if _, err := c.WriteToUDP([]byte(scan), dst); err != nil {
			t.Fatal(err)
		}
		c.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		_, from, err := c.ReadFromUDP(make([]byte, MaxDatagram))
		c.Close()
		if err != nil || !from.IP.Equal(dst.IP) {
			t.Errorf("phone=%s destination=%s reply from=%v err=%v", subnet.phone, dst.IP, from, err)
		}
	}
}

// The address reader is an edge seam: each scan sees the current interface
// state even though the startup selection still names the old address/prefix.
func TestUDPCurrentAddressBeforeBudget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		current     []netip.Prefix
		destination string
	}{
		{"narrowed prefix", []netip.Prefix{netip.MustParsePrefix("127.0.0.2/32")}, "127.0.0.2"},
		{"removed address or lookup failure", nil, "127.0.0.2"},
		{"removed startup address", []netip.Prefix{netip.MustParsePrefix("127.0.0.8/8")}, "127.0.0.2"},
		{"replacement address requires restart", []netip.Prefix{netip.MustParsePrefix("127.0.0.8/8")}, "127.0.0.8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sources := loopbackSources(t)
			for index := range sources {
				sources[index] = []netip.Prefix{netip.MustParsePrefix("127.0.0.2/8")}
			}
			s, err := listen(0, sources)
			if err != nil {
				t.Fatal(err)
			}
			s.addresses = func(int) []netip.Prefix { return tc.current }
			m, _, _ := New("SN", "name", "S50")
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- s.Serve(ctx, m) }()
			defer func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
				// Join before reading the single-owner bucket.
				if m.tokens != ReplyBurst {
					t.Errorf("remaining tokens=%d want=%d", m.tokens, ReplyBurst)
				}
			}()
			c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			destination := s.addr()
			destination.IP = net.ParseIP(tc.destination)
			if _, err := c.WriteToUDP([]byte(scan), destination); err != nil {
				t.Fatal(err)
			}
			c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			_, from, err := c.ReadFromUDP(make([]byte, MaxDatagram))
			if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
				t.Errorf("obsolete selection replied from %v: %v", from, err)
			}
		})
	}
}

// This fixture requires Linux loopback broadcast routing; macOS lo0 does not provide it.
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

