package landiscovery

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/netip"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/net/ipv4"
)

// fakeInterfaces is an interface source with no operating system behind it.
type fakeInterfaces struct {
	list  []net.Interface
	addrs map[int][]string
	fail  map[int]bool // interfaces whose addresses can't be read
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
	if f.fail[iface.Index] {
		return nil, errors.New("interface enumeration failed")
	}
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

// sent is one datagram a fakeConn sent: where to, and the interface and
// source address its packet information named (0 and invalid for none).
type sent struct {
	to    netip.Addr
	index int
	src   netip.Addr
}

// fakeConn is a UDP socket with a scripted network behind it: respond
// returns the replies each send provokes, and writeErr fails sends.
// reading, if set, gets a value each time ReadFrom starts waiting.
type fakeConn struct {
	mu       sync.Mutex
	deadline time.Time
	wake     chan struct{}
	in       chan datagram
	sent     []string
	targets  []netip.Addr
	sends    []sent
	closed   bool
	reading  chan struct{}
	respond  func(to netip.Addr) []datagram
	writeErr func(to netip.Addr) error
}

func newFakeConn() *fakeConn {
	return &fakeConn{wake: make(chan struct{}, 1), in: make(chan datagram, 64)}
}

func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// sentCount is the number of datagrams sent so far.
func (c *fakeConn) sentCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
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
		if c.reading != nil {
			select {
			case c.reading <- struct{}{}:
			default:
			}
		}
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

func (c *fakeConn) WriteTo(b []byte, cm *ipv4.ControlMessage, to net.Addr) (int, error) {
	address := to.(*net.UDPAddr).AddrPort()
	one := sent{to: address.Addr()}
	if cm != nil {
		one.index = cm.IfIndex
		one.src, _ = netip.AddrFromSlice(cm.Src.To4())
	}
	c.mu.Lock()
	c.sent = append(c.sent, string(b))
	c.targets = append(c.targets, address.Addr())
	c.sends = append(c.sends, one)
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
	// Each broadcast goes out of its own interface, from its address.
	en0 := sent{netip.MustParseAddr("192.0.2.255"), 2, netip.MustParseAddr("192.0.2.5")}
	en2 := sent{netip.MustParseAddr("198.51.100.127"), 5, netip.MustParseAddr("198.51.100.7")}
	if !slices.Equal(c.sends, []sent{en0, en2, en0, en2, en0, en2}) {
		t.Fatalf("sends %+v", c.sends)
	}
	if !c.isClosed() {
		t.Fatal("the scan left its socket open")
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
		{"invalid UTF-8 name", "{\"result\":{\"sn\":\"S\",\"ssid\":\"S50\xff_A\"}}", true, Telescope{Serial: "S", Name: "S50\uFFFD_A"}},
		{"invalid UTF-8 model", "{\"result\":{\"sn\":\"S\",\"product_model\":\"Seestar \xc3\"}}", true, Telescope{Serial: "S", Model: "Seestar \uFFFD"}},
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

func TestScanSendsOnEachInterface(t *testing.T) {
	// Two adapters on links that share a subnet, and an alias on one of
	// them in the same subnet: three sends out of each adapter.
	c := newFakeConn()
	s := scanner(c, 50*time.Millisecond)
	s.Interfaces = fakeInterfaces{
		list: []net.Interface{
			{Index: 2, Name: "en0", Flags: net.FlagUp | net.FlagBroadcast},
			{Index: 6, Name: "en5", Flags: net.FlagUp | net.FlagBroadcast},
		},
		addrs: map[int][]string{2: {"192.0.2.5/24", "192.0.2.6/24"}, 6: {"192.0.2.9/24"}},
	}
	if _, err := s.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	broadcast := netip.MustParseAddr("192.0.2.255")
	want := []sent{
		{broadcast, 2, netip.MustParseAddr("192.0.2.5")}, {broadcast, 6, netip.MustParseAddr("192.0.2.9")},
		{broadcast, 2, netip.MustParseAddr("192.0.2.5")}, {broadcast, 6, netip.MustParseAddr("192.0.2.9")},
		{broadcast, 2, netip.MustParseAddr("192.0.2.5")}, {broadcast, 6, netip.MustParseAddr("192.0.2.9")},
	}
	if !slices.Equal(c.sends, want) {
		t.Fatalf("sends %+v, want %+v", c.sends, want)
	}
}

func TestScanOwnAddressesFailClosed(t *testing.T) {
	// The reply comes from this computer's address on an interface whose
	// addresses can't be read: neither a scan nor a probe may accept it.
	for _, failing := range []int{1, 2, 4} {
		interfaces := homeInterfaces()
		interfaces.fail = map[int]bool{failing: true}
		listened := false
		c := newFakeConn()
		c.respond = func(netip.Addr) []datagram {
			return []datagram{{replyBytes("SERIAL-A", "Own proxy", "Seestar S50", 0), netip.MustParseAddr("192.0.2.5")}}
		}
		s := scanner(c, 50*time.Millisecond)
		s.Interfaces = interfaces
		s.Listen = func() (Conn, error) { listened = true; return c, nil }
		r, err := s.Scan(t.Context())
		if !errors.Is(err, errLocal) || len(r.Telescopes) != 0 || listened {
			t.Fatalf("interface %d: result %+v err %v listened %t", failing, r, err, listened)
		}
		if err.Error() != "LAN scan: couldn't determine this computer's addresses" {
			t.Fatalf("error text %q", err)
		}
		if r, err := s.Probe(t.Context(), netip.MustParseAddr("192.0.2.5")); !errors.Is(err, errLocal) || len(r.Telescopes) != 0 || listened {
			t.Fatalf("probe, interface %d: result %+v err %v", failing, r, err)
		}
	}
}

func TestProbeStopsEarly(t *testing.T) {
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
	// A probe names no interface: the routing table chooses.
	if !slices.Equal(c.sends, []sent{{to: target}}) {
		t.Fatalf("probe sent %+v", c.sends)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("probe waited for the window after the answer")
	}
	if !c.isClosed() {
		t.Fatal("the probe left its socket open")
	}
	if _, err := s.Probe(t.Context(), netip.MustParseAddr("2001:db8::1")); err == nil {
		t.Fatal("probed an IPv6 address")
	}
}

func TestScanClosesItsSocket(t *testing.T) {
	quiet := newFakeConn()
	if _, err := scanner(quiet, 20*time.Millisecond).Scan(t.Context()); err != nil || !quiet.isClosed() {
		t.Fatalf("silence: err %v closed %t", err, quiet.isClosed())
	}
	failing := newFakeConn()
	failing.writeErr = func(netip.Addr) error { return errors.New("no buffer space") }
	if _, err := scanner(failing, 20*time.Millisecond).Scan(t.Context()); err == nil || !failing.isClosed() {
		t.Fatalf("failed sends: err %v closed %t", err, failing.isClosed())
	}
	blocked := newFakeConn()
	blocked.writeErr = func(netip.Addr) error { return hostUnreachable() }
	if _, err := scanner(blocked, 20*time.Millisecond).Scan(t.Context()); err != nil || !blocked.isClosed() {
		t.Fatalf("blocked: err %v closed %t", err, blocked.isClosed())
	}
}

func TestScanStopsOnCancel(t *testing.T) {
	// The next send is a minute away: only the cancel can end the read.
	c := newFakeConn()
	c.reading = make(chan struct{}, 1)
	s := scanner(c, 10*time.Minute)
	s.Interval = time.Minute
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type outcome struct {
		err     error
		elapsed time.Duration
	}
	out := make(chan outcome, 1)
	go func() {
		_, err := s.Scan(ctx)
		out <- outcome{err, 0}
	}()
	select {
	case <-c.reading:
	case <-time.After(10 * time.Second):
		t.Fatal("the scan never read")
	}
	start := time.Now()
	cancel()
	select {
	case o := <-out:
		if !errors.Is(o.err, context.Canceled) {
			t.Fatalf("err %v, want context.Canceled", o.err)
		}
		if waited := time.Since(start); waited > 2*time.Second {
			t.Fatalf("the read went on %v after the cancel", waited)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel didn't wake the blocked read")
	}
	if !c.isClosed() {
		t.Fatal("the canceled scan left its socket open")
	}
}

func TestFollow(t *testing.T) {
	keep := func(string) {} // a string session has nothing to release
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
	type reply struct {
		serial, from string
	}
	// network answers each broadcast with replies, in order.
	network := func(window time.Duration, replies ...reply) (*Scanner, *fakeConn) {
		c := newFakeConn()
		c.respond = func(to netip.Addr) []datagram {
			if to != netip.MustParseAddr("192.0.2.255") {
				return nil
			}
			var out []datagram
			for _, r := range replies {
				out = append(out, datagram{replyBytes(r.serial, "S50_A", "Seestar S50", 0), netip.MustParseAddr(r.from)})
			}
			return out
		}
		return scanner(c, window), c
	}
	// dialer connects to the endpoints in ok, and records each it tried.
	dialer := func(ok ...string) (func(context.Context, string) (string, error), func() []string) {
		var mu sync.Mutex
		var tried []string
		return func(_ context.Context, endpoint string) (string, error) {
				mu.Lock()
				tried = append(tried, endpoint)
				mu.Unlock()
				if slices.Contains(ok, endpoint) {
					return "session@" + endpoint, nil
				}
				return "", refused
			}, func() []string {
				mu.Lock()
				defer mu.Unlock()
				return slices.Clone(tried)
			}
	}

	t.Run("saved address answers with the serial", func(t *testing.T) {
		s, c := network(time.Minute, reply{"SERIAL-A", "192.0.2.20"})
		dial, tried := dialer("192.0.2.20:445")
		start := time.Now()
		got, r, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", dial, keep)
		if err != nil || got != "session@192.0.2.20:445" || r.Moved || r.NotSeen || r.Seen == nil || r.Seen.Model != "Seestar S50" ||
			!slices.Equal(tried(), []string{"192.0.2.20:445"}) || c.sentCount() == 0 {
			t.Fatalf("got %q %+v %v tried %v sent %d", got, r, err, tried(), c.sentCount())
		}
		// The scan ends once the telescope connects.
		if time.Since(start) > 10*time.Second || !c.isClosed() {
			t.Fatalf("scan ran on after the connect, or left its socket open")
		}
	})
	t.Run("moved telescope", func(t *testing.T) {
		s, c := network(time.Minute, reply{"SERIAL-A", "192.0.2.30"})
		dial, tried := dialer("192.0.2.20:445", "192.0.2.30:445")
		got, r, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", dial, keep)
		// The saved address didn't answer with the serial: it isn't tried.
		if err != nil || got != "session@192.0.2.30:445" || !r.Moved || r.Endpoint != "192.0.2.30:445" || r.Seen == nil ||
			!slices.Equal(tried(), []string{"192.0.2.30:445"}) || !c.isClosed() {
			t.Fatalf("got %q %+v %v tried %v", got, r, err, tried())
		}
	})
	t.Run("another telescope at the saved address", func(t *testing.T) {
		s, _ := network(100*time.Millisecond, reply{"SERIAL-B", "192.0.2.20"})
		dial, tried := dialer("192.0.2.20:445")
		_, r, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", dial, keep)
		if !errors.Is(err, ErrOtherTelescope) || !r.Conflict || r.Moved || r.Seen != nil || r.NotSeen || len(tried()) != 0 {
			t.Fatalf("%+v %v tried %v", r, err, tried())
		}
	})
	t.Run("another telescope elsewhere is never dialed", func(t *testing.T) {
		s, _ := network(100*time.Millisecond, reply{"SERIAL-B", "192.0.2.30"})
		dial, tried := dialer("192.0.2.30:445")
		_, r, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", dial, keep)
		if err != refused || r.Moved || r.Seen != nil || !r.NotSeen || r.Endpoint != "192.0.2.20:445" ||
			!slices.Equal(tried(), []string{"192.0.2.20:445"}) {
			t.Fatalf("%+v %v tried %v", r, err, tried())
		}
	})
	t.Run("not seen falls back to the saved address", func(t *testing.T) {
		s, c := network(100 * time.Millisecond)
		dial, tried := dialer("192.0.2.20:445")
		got, r, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", dial, keep)
		if err != nil || got != "session@192.0.2.20:445" || !r.NotSeen || r.Moved || r.Seen != nil || c.sentCount() == 0 ||
			!slices.Equal(tried(), []string{"192.0.2.20:445"}) {
			t.Fatalf("got %q %+v %v tried %v", got, r, err, tried())
		}
	})
	t.Run("a failed scan falls back too", func(t *testing.T) {
		s, c := network(100 * time.Millisecond)
		c.writeErr = func(netip.Addr) error { return errors.New("no buffer space") }
		dial, tried := dialer("192.0.2.20:445")
		if _, r, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", dial, keep); err != nil || !r.NotSeen || len(tried()) != 1 {
			t.Fatalf("%+v %v", r, err)
		}
	})
	t.Run("a failed address doesn't end the search", func(t *testing.T) {
		s, _ := network(time.Minute, reply{"SERIAL-A", "192.0.2.30"}, reply{"SERIAL-A", "192.0.2.31"})
		dial, tried := dialer("192.0.2.31:445")
		got, r, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", dial, keep)
		if err != nil || got != "session@192.0.2.31:445" || !r.Moved || !slices.Equal(tried(), []string{"192.0.2.30:445", "192.0.2.31:445"}) {
			t.Fatalf("got %q %+v %v tried %v", got, r, err, tried())
		}
	})
	t.Run("saved address first among the replies waiting", func(t *testing.T) {
		s, c := network(time.Minute, reply{"SERIAL-A", "192.0.2.30"}, reply{"SERIAL-A", "192.0.2.31"}, reply{"SERIAL-A", "192.0.2.20"})
		inner, tried := dialer("192.0.2.20:445", "192.0.2.31:445")
		dial := func(ctx context.Context, endpoint string) (string, error) {
			// Let the scan read every reply before the first dial ends.
			for len(c.in) > 0 {
				time.Sleep(time.Millisecond)
			}
			time.Sleep(50 * time.Millisecond)
			return inner(ctx, endpoint)
		}
		got, r, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", dial, keep)
		if err != nil || got != "session@192.0.2.20:445" || r.Moved || slices.Contains(tried(), "192.0.2.31:445") {
			t.Fatalf("got %q %+v %v tried %v", got, r, err, tried())
		}
	})
	t.Run("seen but nowhere connects", func(t *testing.T) {
		s, _ := network(100*time.Millisecond, reply{"SERIAL-A", "192.0.2.30"})
		dial, tried := dialer()
		_, r, err := Follow(t.Context(), s, "192.0.2.20:445", "SERIAL-A", dial, keep)
		if err != refused || r.Moved || r.NotSeen || r.Seen == nil || r.Endpoint != "192.0.2.20:445" ||
			!slices.Equal(tried(), []string{"192.0.2.30:445"}) {
			t.Fatalf("%+v %v tried %v", r, err, tried())
		}
	})
	t.Run("no serial saved", func(t *testing.T) {
		s, c := network(time.Minute, reply{"SERIAL-A", "192.0.2.30"})
		dial, _ := dialer("192.0.2.30:445")
		if _, _, err := Follow(t.Context(), s, "192.0.2.20:445", "", dial, keep); err != refused || c.sentCount() != 0 {
			t.Fatalf("err %v sent %d", err, c.sentCount())
		}
	})
	t.Run("cancel", func(t *testing.T) {
		s, c := network(time.Minute)
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(20*time.Millisecond, cancel)
		dial, tried := dialer("192.0.2.20:445")
		start := time.Now()
		if _, _, err := Follow(ctx, s, "192.0.2.20:445", "SERIAL-A", dial, keep); !errors.Is(err, context.Canceled) || len(tried()) != 0 {
			t.Fatalf("err %v tried %v", err, tried())
		}
		if time.Since(start) > 10*time.Second || !c.isClosed() {
			t.Fatal("follow ran on after cancel, or left its socket open")
		}
	})
}

// TestDecodeReplyReadsFourMembers holds the reply decoder to the members
// the spec names: a field for any other, the password above all, fails.
func TestDecodeReplyReadsFourMembers(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "scan.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "decodeReply" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range st.Fields.List {
				if field.Tag == nil {
					tags = append(tags, "untagged field")
					continue
				}
				tag, _ := strconv.Unquote(field.Tag.Value)
				tags = append(tags, reflect.StructTag(tag).Get("json"))
			}
			return true
		})
	}
	slices.Sort(tags)
	want := []string{"product_model", "result", "sn", "ssid", "tcp_client_num"}
	if !found || !slices.Equal(tags, want) {
		t.Fatalf("decodeReply reads %q, want %q", tags, want)
	}
}

func FuzzDecodeReply(f *testing.F) {
	for _, s := range []string{
		string(replyBytes("SERIAL-A", "S50_A", "Seestar S50", 1)),
		`{"result":{"sn":"S"}}`,
		`{"result":{"sn":"S","sn":"T","ssid":"a","ssid":7}}`,
		`{"result":{"sn":7,"ssid":"x"}}`,
		`{"result":{"sn":"S","ssid":"S50\xff","product_model":"\xc3"}}`,
		"{\"result\":{\"sn\":\"S\",\"ssid\":\"S50\xff_A\"}}",
		`{"result":{"sn":"S","tcp_client_num":1e400}}`,
		`{"result":{"sn":"S","tcp_client_num":-9223372036854775809}}`,
		`{"result":{"sn":"S","password":{"a":[1,{"b":null}]}}}`,
		`{"result":[{"sn":"S"}]}`,
		`{"result":{"sn":"S"}} trailing`,
		`[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]]`,
		`null`,
		"",
	} {
		f.Add([]byte(s))
	}
	f.Add(append([]byte(`{"result":{"sn":"S","ssid":"`), append(make([]byte, MaxDatagram), `"}}`...)...))
	f.Fuzz(func(t *testing.T, b []byte) {
		got, ok := decodeReply(b)
		if !ok {
			if got.Serial != "" || got.Name != "" || got.Model != "" || got.Clients != 0 {
				t.Fatalf("rejected reply returned %+v", got)
			}
			return
		}
		if got.Serial == "" || got.Addresses != nil {
			t.Fatalf("accepted reply %+v", got)
		}
		// Labels are text the app shows: always valid UTF-8.
		if !utf8.ValidString(got.Name) || !utf8.ValidString(got.Model) {
			t.Fatalf("invalid UTF-8 in %+v", got)
		}
		// The decode is a function of the bytes.
		if again, ok := decodeReply(b); !ok || again.Serial != got.Serial || again.Name != got.Name || again.Model != got.Model || again.Clients != got.Clients {
			t.Fatal("a second decode differed")
		}
	})
}
