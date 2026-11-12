package ui

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"golang.org/x/net/ipv4"
)

// lanNet is a network of telescopes with no sockets behind it: each send
// is answered by the telescopes it reaches.
type lanNet struct {
	mu      sync.Mutex
	answers map[netip.Addr][3]string // serial, ssid, model by address
	clients int
	sendErr error
	sent    []netip.Addr
	listens int // sockets opened: one per scan or probe
	// perScan, when set, answers each socket as the network was when it
	// opened, so a scan already running doesn't see a later set.
	perScan bool
}

func (n *lanNet) scans() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.listens
}

func (n *lanNet) set(answers map[netip.Addr][3]string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.answers = answers
}

func (n *lanNet) sends() []netip.Addr {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]netip.Addr(nil), n.sent...)
}

// conn is a socket on n.
func (n *lanNet) conn() *lanConn {
	n.mu.Lock()
	n.listens++
	var answers map[netip.Addr][3]string
	if n.perScan {
		answers = n.answers
	}
	n.mu.Unlock()
	return &lanConn{n: n, answers: answers, in: make(chan lanDatagram, 64), wake: make(chan struct{}, 1)}
}

// useLAN makes the app's scanner scan n, from a computer at 192.0.2.5/24.
func useLAN(t *testing.T, n *lanNet) {
	t.Helper()
	saved := lanScanner
	t.Cleanup(func() { lanScanner = saved })
	lanScanner = &landiscovery.Scanner{
		Interfaces: lanInterfaces{},
		Listen:     func() (landiscovery.Conn, error) { return n.conn(), nil },
		Interval:   20 * time.Millisecond,
		Window:     200 * time.Millisecond,
	}
}

type lanInterfaces struct{}

var lanIfaces = []net.Interface{{Index: 1, Name: "lo", Flags: net.FlagUp | net.FlagLoopback}, {Index: 2, Name: "en0", Flags: net.FlagUp | net.FlagBroadcast}}

func (lanInterfaces) List() ([]net.Interface, error) { return lanIfaces, nil }
func (lanInterfaces) ByIndex(i int) (*net.Interface, error) {
	return &lanIfaces[i-1], nil
}
func (lanInterfaces) Addrs(iface *net.Interface) ([]net.Addr, error) {
	if iface.Index == 1 {
		return []net.Addr{&net.IPNet{IP: net.IPv4(127, 0, 0, 1), Mask: net.CIDRMask(8, 32)}}, nil
	}
	return []net.Addr{&net.IPNet{IP: net.IPv4(192, 0, 2, 5), Mask: net.CIDRMask(24, 32)}}, nil
}

type lanDatagram struct {
	b    []byte
	from netip.Addr
}

type lanConn struct {
	n        *lanNet
	answers  map[netip.Addr][3]string // n's when it opened, if n.perScan
	mu       sync.Mutex
	deadline time.Time
	closed   bool
	in       chan lanDatagram
	wake     chan struct{}
}

func (c *lanConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		deadline, closed := c.deadline, c.closed
		c.mu.Unlock()
		if closed {
			return 0, nil, net.ErrClosed
		}
		wait := time.Hour
		if !deadline.IsZero() {
			if wait = time.Until(deadline); wait <= 0 {
				return 0, nil, os.ErrDeadlineExceeded
			}
		}
		timer := time.NewTimer(wait)
		select {
		case d := <-c.in:
			timer.Stop()
			return copy(b, d.b), net.UDPAddrFromAddrPort(netip.AddrPortFrom(d.from, landiscovery.Port)), nil
		case <-timer.C:
		case <-c.wake:
			timer.Stop()
		}
	}
}

func (c *lanConn) WriteTo(b []byte, _ *ipv4.ControlMessage, to net.Addr) (int, error) {
	target := to.(*net.UDPAddr).AddrPort().Addr()
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	c.n.sent = append(c.n.sent, target)
	if c.n.sendErr != nil {
		return 0, c.n.sendErr
	}
	answers := c.n.answers
	if c.answers != nil {
		answers = c.answers
	}
	for address, a := range answers {
		if target == address || target == netip.MustParseAddr("192.0.2.255") {
			reply := `{"jsonrpc":"2.0","method":"scan_iscope","result":{"sn":"` + a[0] + `","ssid":"` + a[1] + `","product_model":"` + a[2] +
				`","tcp_client_num":` + strconv.Itoa(c.n.clients) + `,"connect_lock":false,"password":"0123456789abcdef0123456789abcdef"},"code":0,"id":1}`
			select {
			case c.in <- lanDatagram{[]byte(reply), address}:
			default:
			}
		}
	}
	return len(b), nil
}

func (c *lanConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}

func (c *lanConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return nil
}

func twoTelescopes() *lanNet {
	return &lanNet{answers: map[netip.Addr][3]string{
		netip.MustParseAddr("192.0.2.20"): {"SERIAL-A", "S50_A", "Seestar S50"},
		netip.MustParseAddr("192.0.2.40"): {"SERIAL-B", "S30_B", "Seestar S30"},
	}}
}

func scanPost(t *testing.T, s *server, body string) scanView {
	t.Helper()
	w := doPost(s, "/api/scan", body)
	if w.Code != 200 {
		t.Fatalf("scan = %d %s", w.Code, w.Body)
	}
	for _, secret := range []string{"password", "0123456789abcdef"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("scan answer carries %q: %s", secret, w.Body)
		}
	}
	var v scanView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// trailText is Activity's lines and gui.log, which must hold no password
// or lock-password hash.
func trailText(t *testing.T, s *server) string {
	t.Helper()
	events, _, _, _ := s.controller.log.trail()
	b, _ := json.Marshal(events)
	disk, _ := os.ReadFile(filepath.Join(s.storage.root, "gui.log"))
	text := string(b) + string(disk)
	for _, secret := range []string{"password", "0123456789abcdef"} {
		if strings.Contains(text, secret) {
			t.Fatalf("the trail holds %q: %s", secret, text)
		}
	}
	return text
}

func TestScanListsTelescopes(t *testing.T) {
	n := twoTelescopes()
	useLAN(t, n)
	s := testServer(t)
	s.controller.log = &eventLog{storage: s.storage}
	v := scanPost(t, s, `{}`)
	if !v.OK || v.Replies != 6 || v.Blocked || v.Message != "" || len(v.Telescopes) != 2 {
		t.Fatalf("scan %+v", v)
	}
	byName := map[string]scanTelescope{}
	for _, row := range v.Telescopes {
		byName[row.Name] = row
	}
	if a := byName["S50_A"]; a.Model != "Seestar S50" || a.Serial != "SERIAL-A" || len(a.Addresses) != 1 || a.Addresses[0] != "192.0.2.20" || a.Busy || a.Saved {
		t.Fatalf("row %+v", a)
	}
	if b := byName["S30_B"]; b.Serial != "SERIAL-B" {
		t.Fatalf("row %+v", b)
	}
	// A line for the count, then one for each telescope, in the order
	// they answered.
	text := trailText(t, s)
	for _, line := range []string{"Scanned this network: 6 replies.", "Found S50_A (Seestar S50, serial SERIAL-A) at 192.0.2.20.", "Found S30_B (Seestar S30, serial SERIAL-B) at 192.0.2.40."} {
		if !strings.Contains(text, `"message":"`+line+`","code":"scan_listed"`) {
			t.Fatalf("trail lacks %q: %s", line, text)
		}
	}

	n.clients = landiscovery.BusyClients
	if v := scanPost(t, s, `{}`); !v.Telescopes[0].Busy {
		t.Fatalf("busy telescope not marked: %+v", v)
	}
}

func TestScanMessages(t *testing.T) {
	saved := hostOS
	t.Cleanup(func() { hostOS = saved })
	for _, goos := range []string{"darwin", "linux", "windows"} {
		hostOS = goos
		s := testServer(t)

		useLAN(t, &lanNet{})
		v := scanPost(t, s, `{}`)
		if v.Blocked || !strings.HasPrefix(v.Message, scanNone) || strings.Contains(v.Message, "System Settings") != (goos == "darwin") {
			t.Fatalf("%s silence: %+v", goos, v)
		}

		useLAN(t, &lanNet{sendErr: &net.OpError{Op: "write", Net: "udp", Err: os.NewSyscallError("sendto", syscall.EHOSTUNREACH)}})
		v = scanPost(t, s, `{}`)
		// Only on macOS is no route to host a Local Network denial.
		if want := scanNoRoute; !v.Blocked || (goos == "darwin") != strings.HasPrefix(v.Message, scanBlocked) ||
			(goos != "darwin" && v.Message != want) || strings.Contains(v.Message, "System Settings") != (goos == "darwin") {
			t.Fatalf("%s blocked: %+v", goos, v)
		}

		useLAN(t, &lanNet{sendErr: errors.New("synthetic send failure")})
		if v = scanPost(t, s, `{}`); v.Message != scanFailed {
			t.Fatalf("%s failed: %+v", goos, v)
		}
	}
	useLAN(t, &lanNet{})
	lanScanner.Interfaces = noInterfaces{}
	if v := scanPost(t, testServer(t), `{}`); v.Message != scanNoInterface {
		t.Fatalf("no interface: %+v", v)
	}
}

type noInterfaces struct{ lanInterfaces }

func (noInterfaces) List() ([]net.Interface, error) { return lanIfaces[:1], nil }

func TestScanTypedAddress(t *testing.T) {
	n := twoTelescopes()
	useLAN(t, n)
	s := testServer(t)
	v := scanPost(t, s, `{"address":"192.0.2.40:445"}`)
	if len(v.Telescopes) != 1 || v.Telescopes[0].Name != "S30_B" || v.Message != "" {
		t.Fatalf("probe %+v", v)
	}
	for _, sent := range n.sends() {
		if sent != netip.MustParseAddr("192.0.2.40") {
			t.Fatalf("probe sent to %s", sent)
		}
	}
	// Nothing answering a typed address is for Test to explain.
	if v := scanPost(t, s, `{"address":"192.0.2.77"}`); len(v.Telescopes) != 0 || v.Message != "" {
		t.Fatalf("silent probe %+v", v)
	}
	// A name is only tested, never scanned.
	before := len(n.sends())
	if v := scanPost(t, s, `{"address":"seestar.example.invalid"}`); len(v.Telescopes) != 0 || len(n.sends()) != before {
		t.Fatalf("name probe %+v", v)
	}
	if w := doPost(s, "/api/scan", `{"address":"192.0.2.1:99999"}`); w.Code != 400 || !strings.Contains(w.Body.String(), `"lan_address"`) {
		t.Fatalf("bad address = %d %s", w.Code, w.Body)
	}
}

// Test's question of whether a serial answered at a typed address: what
// a scan heard there, or else a probe of it, unless set never to scan.
func TestSerialAt(t *testing.T) {
	n := twoTelescopes()
	useLAN(t, n)
	s := testServer(t)
	if !s.serialAt(t.Context(), "192.0.2.40") || len(n.sends()) == 0 {
		t.Fatalf("a probe that heard a serial: sent %v", n.sends())
	}
	if serial, ok := s.lan.serial(netip.MustParseAddr("192.0.2.40")); !ok || serial != "SERIAL-B" {
		t.Fatalf("the probe wasn't remembered: %q", serial)
	}
	if s.serialAt(t.Context(), "192.0.2.77") || s.serialAt(t.Context(), "seestar.example.invalid") {
		t.Fatal("a serial where none answered")
	}
	s.mu.Lock()
	s.settings.LANScan = string(landiscovery.ScanOff)
	s.mu.Unlock()
	before := len(n.sends())
	if !s.serialAt(t.Context(), "192.0.2.40") || s.serialAt(t.Context(), "192.0.2.20") || len(n.sends()) != before {
		t.Fatalf("set never to scan: sent %v", n.sends()[before:])
	}
}

// Saving an address a scan saw saves its serial with it.
func TestSaveRouteKeepsScannedSerial(t *testing.T) {
	useLAN(t, twoTelescopes())
	s, _ := copyServer(t)
	scanPost(t, s, `{}`)
	saved := func() Settings {
		t.Helper()
		got, _, err := loadSettings(s.storage)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if err := s.saveRoute(t.Context(), routeLAN, "192.0.2.20"); err != nil {
		t.Fatal(err)
	}
	if got := saved(); got.LANAddress != "192.0.2.20:445" || got.LANSerial != "SERIAL-A" {
		t.Fatalf("saved %+v", got)
	}
	if v := scanPost(t, s, `{}`); !(v.Telescopes[0].Saved != v.Telescopes[1].Saved) {
		t.Fatalf("the saved telescope isn't marked once: %+v", v)
	}
	// The same address again, with nothing scanned since a restart, keeps it.
	s.lan = lanState{}
	if err := s.saveRoute(t.Context(), routeLAN, "192.0.2.20:445"); err != nil {
		t.Fatal(err)
	}
	if got := saved(); got.LANSerial != "SERIAL-A" {
		t.Fatalf("resave lost the serial: %+v", got)
	}
	// The account route keeps it for a switch back.
	if err := s.saveRoute(t.Context(), routeAccount, ""); err != nil {
		t.Fatal(err)
	}
	if got := saved(); got.LANSerial != "SERIAL-A" || got.LANAddress != "192.0.2.20:445" {
		t.Fatalf("account route dropped the address: %+v", got)
	}
	// An address no scan saw has no serial.
	if err := s.saveRoute(t.Context(), routeLAN, "192.0.2.21"); err != nil {
		t.Fatal(err)
	}
	if got := saved(); got.LANSerial != "" {
		t.Fatalf("another address kept the serial: %+v", got)
	}
}

func TestLANSerialIsOptional(t *testing.T) {
	st := testStorage(t)
	old := testSettings()
	old.Route, old.LANAddress, old.RouteEstablished = routeLAN, "192.0.2.20:445", true
	if err := st.write("config.json", old); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(st.root, "config.json"))
	if strings.Contains(string(b), "lan_serial") {
		t.Fatalf("an empty serial was written: %s", b)
	}
	got, _, err := loadSettings(st)
	if err != nil || got.LANSerial != "" || got.LANAddress != old.LANAddress {
		t.Fatalf("%+v %v", got, err)
	}
	old.LANSerial = "SERIAL-A"
	if err := st.write("config.json", old); err != nil {
		t.Fatal(err)
	}
	if got, _, err := loadSettings(st); err != nil || got.LANSerial != "SERIAL-A" {
		t.Fatalf("%+v %v", got, err)
	}
}

// The largest scan the app keeps, every telescope with a long name and
// model, leaves gui.log within its limit and taking lines.
func TestScanLeavesTheLogAppendable(t *testing.T) {
	// The bubble's clock keeps a busy machine from ending the scan early.
	synctest.Test(t, func(t *testing.T) {
		n := &lanNet{answers: make(map[netip.Addr][3]string)}
		const most = 64 // the telescopes a scan keeps
		for i := range most {
			n.answers[netip.AddrFrom4([4]byte{192, 0, 2, byte(20 + i)})] = [3]string{fmt.Sprintf("S%02d", i), strings.Repeat("星", 400), strings.Repeat("型", 400)}
		}
		useLAN(t, n)
		s := testServer(t)
		s.controller.log = &eventLog{storage: s.storage}
		if v, err := s.scan(t.Context(), ""); err != nil || len(v.Telescopes) != most {
			t.Fatalf("scan %d telescopes, %v", len(v.Telescopes), err)
		}
		s.controller.log.add("App opened.")
		info, err := os.Stat(filepath.Join(s.storage.root, "gui.log"))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, failed := s.controller.log.snapshot(); info.Size() > logLimit || failed {
			t.Fatalf("gui.log is %d bytes of %d; an append failed: %v", info.Size(), logLimit, failed)
		}
		if text := trailText(t, s); !strings.Contains(text, "App opened.") {
			t.Fatalf("trail %s", text)
		}
	})
}

// The page's probe of a typed address and Test's serial hook share one
// probe: one socket, one result. A second probe that would hear nothing
// can't make Test miss the serial the page's probe heard.
func TestTestSharesThePagesProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := twoTelescopes()
		useLAN(t, n)
		s := testServer(t)
		listen := lanScanner.Listen
		opened, resume := make(chan struct{}, 2), make(chan struct{})
		var calls int
		lanScanner.Listen = func() (landiscovery.Conn, error) {
			calls++
			opened <- struct{}{}
			if calls == 1 {
				<-resume
				return listen()
			}
			// A second probe would hear nothing.
			return (&lanNet{}).conn(), nil
		}
		scanned := make(chan scanView, 1)
		go func() {
			v, _ := s.scan(t.Context(), "192.0.2.40")
			scanned <- v
		}()
		<-opened
		heard := make(chan bool, 1)
		go func() { heard <- s.serialAt(t.Context(), "192.0.2.40") }()
		synctest.Wait()
		close(resume)
		if v, ok := <-scanned, <-heard; len(v.Telescopes) != 1 || !ok || len(opened) != 0 || calls != 1 {
			t.Fatalf("page heard %+v, Test heard %v, %d probes", v.Telescopes, ok, calls)
		}
		if serial, _ := s.lan.serial(netip.MustParseAddr("192.0.2.40")); serial != "SERIAL-B" {
			t.Fatalf("remembered %q", serial)
		}
	})
}

// A caller that stops waiting doesn't end the shared probe for another.
func TestSharedProbeOutlivesACaller(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		n := twoTelescopes()
		useLAN(t, n)
		s := testServer(t)
		listen := lanScanner.Listen
		resume := make(chan struct{})
		lanScanner.Listen = func() (landiscovery.Conn, error) {
			<-resume
			return listen()
		}
		ctx, cancel := context.WithCancel(t.Context())
		scanned := make(chan error, 1)
		go func() {
			_, err := s.scan(ctx, "192.0.2.40")
			scanned <- err
		}()
		synctest.Wait()
		heard := make(chan bool, 1)
		go func() { heard <- s.serialAt(t.Context(), "192.0.2.40") }()
		synctest.Wait()
		cancel()
		if err := <-scanned; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		close(resume)
		if !<-heard || n.scans() != 1 {
			t.Fatalf("Test didn't hear the serial; %d probes", n.scans())
		}
	})
}
