package landiscovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakeInterfaces is an interface source with no operating system behind it.
type fakeInterfaces struct {
	list  []net.Interface
	addrs map[int][]string
}

func (f fakeInterfaces) List() ([]net.Interface, error) { return f.list, nil }
func (f fakeInterfaces) ByIndex(index int) (*net.Interface, error) {
	for _, iface := range f.list {
		if iface.Index == index {
			return &iface, nil
		}
	}
	return nil, errors.New("no such interface")
}
func (f fakeInterfaces) Addrs(iface *net.Interface) ([]net.Addr, error) {
	var out []net.Addr
	for _, s := range f.addrs[iface.Index] {
		prefix := netip.MustParsePrefix(s)
		out = append(out, &net.IPNet{IP: prefix.Addr().AsSlice(), Mask: net.CIDRMask(prefix.Bits(), prefix.Addr().BitLen())})
	}
	return out, nil
}

// homeInterfaces is a computer at 192.0.2.5 on a /24, with loopback, a
// point-to-point VPN, a down adapter and a second LAN at 198.51.100.7.
func homeInterfaces() fakeInterfaces {
	return fakeInterfaces{
		list: []net.Interface{
			{Index: 1, Name: "lo", Flags: net.FlagUp | net.FlagLoopback},
			{Index: 2, Name: "en0", Flags: net.FlagUp | net.FlagBroadcast | net.FlagMulticast},
			{Index: 3, Name: "utun0", Flags: net.FlagUp | net.FlagPointToPoint | net.FlagBroadcast},
			{Index: 4, Name: "en1", Flags: net.FlagBroadcast},
			{Index: 5, Name: "en2", Flags: net.FlagUp | net.FlagBroadcast},
		},
		addrs: map[int][]string{
			1: {"127.0.0.1/8", "::1/128"},
			2: {"192.0.2.5/24", "fe80::1/64"},
			3: {"203.0.113.9/24"},
			4: {"203.0.113.77/24"},
			5: {"198.51.100.7/25"},
		},
	}
}

type datagram struct {
	b    []byte
	from netip.Addr
}

// fakeConn is a UDP socket with a scripted network behind it: respond
// returns the replies each send provokes, and writeErr fails sends.
type fakeConn struct {
	mu       sync.Mutex
	deadline time.Time
	wake     chan struct{}
	in       chan datagram
	sent     []string
	targets  []netip.Addr
	closed   bool
	respond  func(to netip.Addr) []datagram
	writeErr func(to netip.Addr) error
}

func newFakeConn() *fakeConn {
	return &fakeConn{wake: make(chan struct{}, 1), in: make(chan datagram, 64)}
}

func (c *fakeConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		deadline, closed := c.deadline, c.closed
		c.mu.Unlock()
		if closed {
			return 0, nil, net.ErrClosed
		}
		wait := time.Hour
		if !deadline.IsZero() {
			wait = time.Until(deadline)
			if wait <= 0 {
				return 0, nil, os.ErrDeadlineExceeded
			}
		}
		timer := time.NewTimer(wait)
		select {
		case d := <-c.in:
			timer.Stop()
			n := copy(b, d.b)
			return n, net.UDPAddrFromAddrPort(netip.AddrPortFrom(d.from, Port)), nil
		case <-timer.C:
		case <-c.wake:
			timer.Stop()
		}
	}
}

func (c *fakeConn) WriteTo(b []byte, to net.Addr) (int, error) {
	address := to.(*net.UDPAddr).AddrPort()
	c.mu.Lock()
	c.sent = append(c.sent, string(b))
	c.targets = append(c.targets, address.Addr())
	c.mu.Unlock()
	if address.Port() != Port {
		return 0, errors.New("wrong port")
	}
	if c.writeErr != nil {
		if err := c.writeErr(address.Addr()); err != nil {
			return 0, err
		}
	}
	if c.respond != nil {
		for _, d := range c.respond(address.Addr()) {
			c.in <- d
		}
	}
	return len(b), nil
}

func (c *fakeConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}

func scanner(c *fakeConn, window time.Duration) *Scanner {
	return &Scanner{
		Interfaces: homeInterfaces(),
		Listen:     func() (Conn, error) { return c, nil },
		Sends:      3,
		Interval:   window / 10,
		Window:     window,
	}
}

func replyBytes(sn, ssid, model string, clients int) []byte {
	return []byte(`{"jsonrpc":"2.0","Timestamp":"1.000000001","method":"scan_iscope","result":{"sn":"` + sn + `","ssid":"` + ssid +
		`","product_model":"` + model + `","is_verified":true,"tcp_client_num":` + strconv.Itoa(clients) +
		`,"connect_lock":false,"password":"0123456789abcdef0123456789abcdef"},"code":0,"id":1}`)
}

func TestScanBroadcastsAndCollects(t *testing.T) {
	c := newFakeConn()
	c.respond = func(to netip.Addr) []datagram {
		if to != netip.MustParseAddr("192.0.2.255") {
			return nil
		}
		return []datagram{
			{replyBytes("SERIAL-A", "S50_A", "Seestar S50", 1), netip.MustParseAddr("192.0.2.20")},
			// The house relay answers for the same telescope.
			{replyBytes("SERIAL-A", "S50_A", "Seestar S50", 1), netip.MustParseAddr("192.0.2.30")},
			{replyBytes("SERIAL-B", "S30_B", "Seestar S30", 8), netip.MustParseAddr("192.0.2.40")},
			// A proxy on this computer, no serial, not JSON, and oversize.
			{replyBytes("SERIAL-C", "S50_C", "Seestar S50", 0), netip.MustParseAddr("192.0.2.5")},
			{[]byte(`{"result":{"sn":""}}`), netip.MustParseAddr("192.0.2.41")},
			{[]byte(`{"result":{"ssid":"x"}}`), netip.MustParseAddr("192.0.2.42")},
			{[]byte(`not json`), netip.MustParseAddr("192.0.2.43")},
			{append([]byte(`{"result":{"sn":"BIG"}}`), make([]byte, MaxDatagram)...), netip.MustParseAddr("192.0.2.44")},
		}
	}
	result, err := scanner(c, 200*time.Millisecond).Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.sent) != 6 {
		t.Fatalf("sent %d datagrams, want 3 to each of 2 broadcasts: %q", len(c.sent), c.sent)
	}
	if c.sent[0] != `{"id":1,"method":"scan_iscope","params":""}`+"\r\n" {
		t.Fatalf("request %q", c.sent[0])
	}
	want := []netip.Addr{netip.MustParseAddr("192.0.2.255"), netip.MustParseAddr("198.51.100.127")}
	for _, target := range c.targets {
		if !slices.Contains(want, target) {
			t.Fatalf("sent to %s; want only %v", target, want)
		}
	}
	if result.Blocked || result.Replies != 9 {
		t.Fatalf("replies %d blocked %t, want 9 and false", result.Replies, result.Blocked)
	}
	if len(result.Telescopes) != 2 {
		t.Fatalf("telescopes %+v", result.Telescopes)
	}
	a, b := result.Telescopes[0], result.Telescopes[1]
	if a.Serial != "SERIAL-A" || a.Name != "S50_A" || a.Model != "Seestar S50" || a.Busy() ||
		!slices.Equal(a.Addresses, []netip.Addr{netip.MustParseAddr("192.0.2.20"), netip.MustParseAddr("192.0.2.30")}) {
		t.Fatalf("first telescope %+v", a)
	}
	if b.Serial != "SERIAL-B" || !b.Busy() {
		t.Fatalf("second telescope %+v", b)
	}
}

func TestDecodeReply(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		ok   bool
		want Telescope
	}{
		{"minimal", `{"result":{"sn":"S"}}`, true, Telescope{Serial: "S"}},
		{"wrong types read as absent", `{"result":{"sn":"S","ssid":5,"product_model":null,"tcp_client_num":"9"}}`, true, Telescope{Serial: "S"}},
		{"password of any type is ignored", `{"result":{"sn":"S","password":{"x":1}}}`, true, Telescope{Serial: "S"}},
		{"unknown members", `{"x":[1],"result":{"sn":"S","custom_model":"m","serc":"WPA-PSK"}}`, true, Telescope{Serial: "S"}},
		{"duplicate names", `{"result":{"sn":"S","ssid":"a","ssid":"b"}}`, true, Telescope{Serial: "S", Name: "b"}},
		{"sn not a string", `{"result":{"sn":7}}`, false, Telescope{}},
		{"no result", `{"sn":"S"}`, false, Telescope{}},
		{"result null", `{"result":null}`, false, Telescope{}},
		{"array", `[]`, false, Telescope{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := decodeReply([]byte(tc.in))
			if ok != tc.ok || got.Serial != tc.want.Serial || got.Name != tc.want.Name || got.Model != tc.want.Model || got.Clients != tc.want.Clients {
				t.Fatalf("got %+v %t, want %+v %t", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func hostUnreachable() error {
	return &net.OpError{Op: "write", Net: "udp", Err: os.NewSyscallError("sendto", syscall.EHOSTUNREACH)}
}

func TestScanTellsBlockedFromSilence(t *testing.T) {
	c := newFakeConn()
	c.writeErr = func(netip.Addr) error { return hostUnreachable() }
	result, err := scanner(c, 50*time.Millisecond).Scan(t.Context())
	if err != nil || !result.Blocked {
		t.Fatalf("blocked send: result %+v err %v", result, err)
	}

	quiet := newFakeConn()
	result, err = scanner(quiet, 50*time.Millisecond).Scan(t.Context())
	if err != nil || result.Blocked || result.Replies != 0 {
		t.Fatalf("silence: result %+v err %v", result, err)
	}

	failing := newFakeConn()
	failing.writeErr = func(to netip.Addr) error {
		return &net.OpError{Op: "write", Net: "udp", Addr: net.UDPAddrFromAddrPort(netip.AddrPortFrom(to, Port)), Err: errors.New("no buffer space")}
	}
	_, err = scanner(failing, 50*time.Millisecond).Scan(t.Context())
	if err == nil {
		t.Fatal("every send failed and the scan reported no error")
	}
	// Its text names no address, so it can be logged.
	if strings.Contains(err.Error(), "192.0.2.") || strings.Contains(err.Error(), "198.51.100.") || !strings.Contains(err.Error(), "no buffer space") {
		t.Fatalf("scan error %q", err)
	}

	// One interface failing is not the scan failing.
	partial := newFakeConn()
	partial.writeErr = func(to netip.Addr) error {
		if to == netip.MustParseAddr("198.51.100.127") {
			return errors.New("no buffer space")
		}
		return nil
	}
	if _, err := scanner(partial, 50*time.Millisecond).Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestScanWithoutInterface(t *testing.T) {
	s := scanner(newFakeConn(), 50*time.Millisecond)
	s.Interfaces = fakeInterfaces{list: []net.Interface{{Index: 1, Flags: net.FlagUp | net.FlagLoopback}}, addrs: map[int][]string{1: {"127.0.0.1/8"}}}
	if _, err := s.Scan(t.Context()); !errors.Is(err, ErrNoInterface) {
		t.Fatalf("err %v, want ErrNoInterface", err)
	}
}

func TestProbeAndFindStopEarly(t *testing.T) {
	target := netip.MustParseAddr("192.0.2.20")
	c := newFakeConn()
	c.respond = func(to netip.Addr) []datagram {
		return []datagram{
			{replyBytes("OTHER", "x", "m", 0), netip.MustParseAddr("192.0.2.21")},
			{replyBytes("SERIAL-A", "S50_A", "Seestar S50", 2), target},
		}
	}
	s := scanner(c, time.Minute)
	start := time.Now()
	result, err := s.Probe(t.Context(), target)
	if err != nil || len(result.Telescopes) != 1 || result.Telescopes[0].Serial != "SERIAL-A" || result.Telescopes[0].Clients != 2 {
		t.Fatalf("probe: %+v %v", result, err)
	}
	if !slices.Equal(c.targets, []netip.Addr{target}) {
		t.Fatalf("probe sent to %v", c.targets)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("probe waited for the window after the answer")
	}

	c = newFakeConn()
	c.respond = func(netip.Addr) []datagram {
		return []datagram{{replyBytes("SERIAL-A", "S50_A", "Seestar S50", 0), target}}
	}
	s = scanner(c, time.Minute)
	start = time.Now()
	result, err = s.Find(t.Context(), "SERIAL-A")
	if found, ok := result.Find("SERIAL-A"); err != nil || !ok || found.Addresses[0] != target {
		t.Fatalf("find: %+v %v", result, err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("find waited for the window after the answer")
	}
	if _, err := s.Probe(t.Context(), netip.MustParseAddr("2001:db8::1")); err == nil {
		t.Fatal("probed an IPv6 address")
	}
}

func TestScanStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(20*time.Millisecond, cancel)
	start := time.Now()
	_, err := scanner(newFakeConn(), time.Minute).Scan(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v, want context.Canceled", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("scan ran on after cancel")
	}
}

func TestRedial(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	moved := netip.MustParseAddr("192.0.2.30")
	newScanner := func() (*Scanner, *fakeConn) {
		c := newFakeConn()
		c.respond = func(netip.Addr) []datagram {
			return []datagram{{replyBytes("SERIAL-A", "S50_A", "Seestar S50", 0), moved}}
		}
		return scanner(c, time.Minute), c
	}
	dialer := func(ok string, err error) (func(context.Context, string) (string, error), *[]string) {
		var tried []string
		return func(_ context.Context, endpoint string) (string, error) {
			tried = append(tried, endpoint)
			if endpoint == ok {
				return "session@" + endpoint, nil
			}
			return "", err
		}, &tried
	}

	t.Run("saved address works", func(t *testing.T) {
		s, c := newScanner()
		dial, tried := dialer("192.0.2.20:445", refused)
		got, r, err := Redial(t.Context(), s, "192.0.2.20:445", "SERIAL-A", dial)
		if err != nil || got != "session@192.0.2.20:445" || r.Moved || r.Seen != nil || len(*tried) != 1 || len(c.sent) != 0 {
			t.Fatalf("got %q %+v %v tried %v sent %d", got, r, err, *tried, len(c.sent))
		}
	})
	t.Run("moved telescope", func(t *testing.T) {
		s, _ := newScanner()
		dial, tried := dialer("192.0.2.30:445", refused)
		got, r, err := Redial(t.Context(), s, "192.0.2.20:445", "SERIAL-A", dial)
		if err != nil || got != "session@192.0.2.30:445" || !r.Moved || r.Endpoint != "192.0.2.30:445" || r.Seen == nil || r.Seen.Model != "Seestar S50" || r.Replies != 1 {
			t.Fatalf("got %q %+v %v tried %v", got, r, err, *tried)
		}
	})
	t.Run("no serial saved", func(t *testing.T) {
		s, c := newScanner()
		dial, _ := dialer("192.0.2.30:445", refused)
		if _, _, err := Redial(t.Context(), s, "192.0.2.20:445", "", dial); err != refused || len(c.sent) != 0 {
			t.Fatalf("err %v sent %d", err, len(c.sent))
		}
	})
	t.Run("server answered", func(t *testing.T) {
		s, c := newScanner()
		denied := errors.New("logon failure")
		dial, _ := dialer("192.0.2.30:445", denied)
		if _, _, err := Redial(t.Context(), s, "192.0.2.20:445", "SERIAL-A", dial); err != denied || len(c.sent) != 0 {
			t.Fatalf("err %v sent %d", err, len(c.sent))
		}
	})
	t.Run("serial not found", func(t *testing.T) {
		s, _ := newScanner()
		s.Window = 50 * time.Millisecond
		s.Interval = 5 * time.Millisecond
		dial, _ := dialer("none", refused)
		_, r, err := Redial(t.Context(), s, "192.0.2.20:445", "SERIAL-Z", dial)
		if err != refused || r.Moved || r.Endpoint != "192.0.2.20:445" {
			t.Fatalf("%+v %v", r, err)
		}
	})
	t.Run("found at the saved address only", func(t *testing.T) {
		s, _ := newScanner()
		dial, tried := dialer("none", refused)
		_, r, err := Redial(t.Context(), s, "192.0.2.30:445", "SERIAL-A", dial)
		if err != refused || r.Moved || len(*tried) != 1 {
			t.Fatalf("%+v %v tried %v", r, err, *tried)
		}
	})
}
