package cli

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
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

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

// lanNet is a network of telescopes with no sockets behind it: each send
// is answered by the telescopes it reaches.
type lanNet struct {
	mu      sync.Mutex
	answers map[netip.Addr][3]string // serial, ssid, model by address
	clients int
	sendErr error
}

// useLAN makes the commands' scanner scan n, from a computer at
// 192.0.2.5/24.
func useLAN(t *testing.T, n *lanNet) {
	t.Helper()
	saved := lanScanner
	t.Cleanup(func() { lanScanner = saved })
	lanScanner = &landiscovery.Scanner{
		Interfaces: lanInterfaces{},
		Listen: func() (landiscovery.Conn, error) {
			return &lanConn{n: n, in: make(chan lanDatagram, 64), wake: make(chan struct{}, 1)}, nil
		},
		Interval: 20 * time.Millisecond,
		Window:   200 * time.Millisecond,
	}
}

type lanInterfaces struct{}

var lanIfaces = []net.Interface{{Index: 1, Name: "lo", Flags: net.FlagUp | net.FlagLoopback}, {Index: 2, Name: "en0", Flags: net.FlagUp | net.FlagBroadcast}}

func (lanInterfaces) List() ([]net.Interface, error)        { return lanIfaces, nil }
func (lanInterfaces) ByIndex(i int) (*net.Interface, error) { return &lanIfaces[i-1], nil }
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

func (c *lanConn) WriteTo(b []byte, to net.Addr) (int, error) {
	target := to.(*net.UDPAddr).AddrPort().Addr()
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	if c.n.sendErr != nil {
		return 0, c.n.sendErr
	}
	for address, a := range c.n.answers {
		if target == address || target == netip.MustParseAddr("192.0.2.255") {
			reply := `{"jsonrpc":"2.0","method":"scan_iscope","result":{"sn":"` + a[0] + `","ssid":"` + a[1] + `","product_model":"` + a[2] +
				`","tcp_client_num":` + strconv.Itoa(c.n.clients) + `,"password":"0123456789abcdef0123456789abcdef"},"code":0,"id":1}`
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
		netip.MustParseAddr("192.0.2.40"): {"SERIAL-B", "S30 B", "Seestar S30"},
	}}
}

// useGOOS makes scan give goos's hints for the rest of the test.
func useGOOS(t *testing.T, goos string) {
	saved := scanGOOS
	t.Cleanup(func() { scanGOOS = saved })
	scanGOOS = goos
}

// scanRun runs scan, leaving the read-only guard off afterward, and checks
// that neither stream carries a serial or the password.
func scanRun(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Cleanup(readonly.Reset)
	code, stdout, stderr = invoke(t, append([]string{"scan"}, args...)...)
	for _, secret := range []string{"SERIAL-", "password", "0123456789abcdef"} {
		if strings.Contains(stdout+stderr, secret) {
			t.Fatalf("scan wrote %q:\n%s\n%s", secret, stdout, stderr)
		}
	}
	return code, stdout, stderr
}

func TestScanText(t *testing.T) {
	n := twoTelescopes()
	n.clients = 8
	useLAN(t, n)
	code, out, errs := scanRun(t)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	slices.Sort(lines)
	want := []string{`192.0.2.20 busy S50_A "Seestar S50"`, `192.0.2.40 busy "S30 B" "Seestar S30"`}
	if code != exitOK || !slices.Equal(lines, want) {
		t.Errorf("exit %d lines %q, want %q\n%s", code, lines, want, errs)
	}
	if !strings.Contains(errs, `msg="scanned this network" replies=6 telescopes=2`) || strings.Contains(errs, "192.0.2.") {
		t.Errorf("logs: %s", errs)
	}
}

func TestScanJSON(t *testing.T) {
	useLAN(t, twoTelescopes())
	code, out, errs := scanRun(t, "--format", "json")
	var got struct {
		Telescopes []scanned `json:"telescopes"`
		Replies    int       `json:"replies"`
	}
	if err := json.Unmarshal([]byte(out), &got, json.RejectUnknownMembers(true)); err != nil || code != exitOK {
		t.Fatalf("exit %d %v: %s\n%s", code, err, out, errs)
	}
	slices.SortFunc(got.Telescopes, func(a, b scanned) int { return strings.Compare(a.Name, b.Name) })
	if got.Replies != 6 || len(got.Telescopes) != 2 || got.Telescopes[0].Name != "S30 B" || got.Telescopes[0].Model != "Seestar S30" ||
		!slices.Equal(got.Telescopes[1].Addresses, []string{"192.0.2.20"}) || got.Telescopes[1].Busy {
		t.Errorf("= %+v", got)
	}
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Errorf("not one line: %q", out)
	}
}

// Nothing answering is a scan that ran: exit 0, an empty list and a
// warning, with the Local Network hint on macOS only.
func TestScanSilent(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		useGOOS(t, goos)
		useLAN(t, &lanNet{})
		code, out, errs := scanRun(t)
		if code != exitOK || out != "" || !strings.Contains(errs, "no telescope answered") || strings.Contains(errs, "Local Network") != (goos == "darwin") {
			t.Errorf("%s: exit %d out %q logs %s", goos, code, out, errs)
		}
		code, out, _ = scanRun(t, "--format=json")
		if code != exitOK || out != `{"telescopes":[],"replies":0}`+"\n" {
			t.Errorf("%s json: exit %d out %q", goos, code, out)
		}
	}
}

// A send the system refuses with no route to host is not "nothing
// answered": scan fails and, on macOS, names the Local Network setting.
func TestScanBlocked(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		useGOOS(t, goos)
		useLAN(t, &lanNet{sendErr: &net.OpError{Op: "write", Net: "udp", Err: os.NewSyscallError("sendto", syscall.EHOSTUNREACH)}})
		code, out, errs := scanRun(t)
		if code != exitFailure || out != "" || !strings.Contains(errs, "sending was refused") ||
			strings.Contains(errs, "Local Network") != (goos == "darwin") || strings.Contains(errs, "no telescope answered") {
			t.Errorf("%s: exit %d out %q logs %s", goos, code, out, errs)
		}
	}
}

// Every send failing otherwise is a failed scan whose error names no
// address.
func TestScanSendFails(t *testing.T) {
	useLAN(t, &lanNet{sendErr: &net.OpError{Op: "write", Net: "udp", Addr: net.UDPAddrFromAddrPort(netip.MustParseAddrPort("192.0.2.255:4720")),
		Err: os.NewSyscallError("sendto", syscall.ENOBUFS)}})
	code, out, errs := scanRun(t)
	if code != exitFailure || out != "" || !strings.Contains(errs, "LAN scan") || strings.Contains(errs, "192.0.2.") {
		t.Errorf("exit %d out %q logs %s", code, out, errs)
	}
}

func TestScanUsage(t *testing.T) {
	useLAN(t, &lanNet{})
	for _, args := range [][]string{{"--format", "jsonl"}, {"--format=PRIVATE_VALUE"}, {"extra"}, {"--profile", "default"}, {"--endpoint", "h:445"}} {
		code, out, errs := scanRun(t, args...)
		if code != exitUsage || out != "" || strings.Contains(errs, "PRIVATE_VALUE") {
			t.Errorf("%q: exit %d out %q logs %s", args, code, out, errs)
		}
	}
	if _, _, errs := scanRun(t, "--format", "xml"); !strings.Contains(errs, `option=--format rule="use text or json"`) {
		t.Errorf("--format xml: %s", errs)
	}
}

func TestScanHelp(t *testing.T) {
	code, out, _ := scanRun(t, "--help")
	for _, want := range []string{"UDP port 4720", "--format json", "serial number", "Local Network", "--endpoint ADDRESS:445"} {
		if code != exitOK || !strings.Contains(out, want) {
			t.Errorf("help lacks %q: exit %d\n%s", want, code, out)
		}
	}
}

// A run follows the telescope only at the endpoint the profile filled, and
// only with a saved serial.
func TestNewFollow(t *testing.T) {
	s := Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A"}
	if f := newFollow(s, none); f == nil || f.saved != s.Endpoint || f.at != s.Endpoint || f.serial != "SERIAL-A" {
		t.Errorf("filled = %+v", f)
	}
	if f := newFollow(s, given("endpoint")); f != nil {
		t.Errorf("given --endpoint = %+v", f)
	}
	if f := newFollow(Saved{Endpoint: s.Endpoint}, none); f != nil {
		t.Errorf("no serial = %+v", f)
	}
	if f := newFollow(Saved{LANSerial: "SERIAL-A"}, none); f != nil {
		t.Errorf("no endpoint = %+v", f)
	}
	var fo findOptions
	fo.fillSaved(s, none)
	var so syncOptions
	so.fillSaved(s, given("endpoint"))
	var bo browseOptions
	bo.fillSaved(s, none)
	var do deleteOptions
	do.fillSaved(s, none)
	if fo.follow == nil || so.follow != nil || bo.follow == nil || do.follow == nil {
		t.Errorf("fillSaved follow: find %v sync %v browse %v delete %v", fo.follow, so.follow, bo.follow, do.follow)
	}
}

// fakeDial answers at the addresses in up and gets no answer elsewhere.
func fakeDial(calls *[]string, up ...string) func(context.Context, string, *slog.Logger) (string, error) {
	return func(_ context.Context, endpoint string, _ *slog.Logger) (string, error) {
		*calls = append(*calls, endpoint)
		if slices.Contains(up, endpoint) {
			return "session at " + endpoint, nil
		}
		return "", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	}
}

func TestFollowDial(t *testing.T) {
	ctx := context.Background()
	useLAN(t, twoTelescopes())
	var logs bytes.Buffer
	log := diagnostic.Logger("text", &logs, slog.LevelInfo)
	f := newFollow(Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A"}, none)

	// Moved: the scan finds the serial, the run connects there and keeps
	// using it.
	var calls []string
	got, err := followDial(ctx, f, "192.0.2.7:445", log, fakeDial(&calls, "192.0.2.20:445"))
	if err != nil || got != "session at 192.0.2.20:445" || !slices.Equal(calls, []string{"192.0.2.7:445", "192.0.2.20:445"}) {
		t.Fatalf("moved = %q, %v; calls %q", got, err, calls)
	}
	if !strings.Contains(logs.String(), `msg="found the saved telescope at a new address on this network" model="Seestar S50" replies=`) {
		t.Errorf("logs: %s", logs.String())
	}
	calls = nil
	if got, err = followDial(ctx, f, "192.0.2.7:445", log, fakeDial(&calls, "192.0.2.20:445")); err != nil || !slices.Equal(calls, []string{"192.0.2.20:445"}) {
		t.Errorf("the next dial = %q, %v; calls %q", got, err, calls)
	}

	// Another endpoint, or no follow, is dialed as it is.
	calls = nil
	if _, err = followDial(ctx, f, "198.51.100.9:445", log, fakeDial(&calls)); !landiscovery.Unreached(err) || !slices.Equal(calls, []string{"198.51.100.9:445"}) {
		t.Errorf("another endpoint: %v; calls %q", err, calls)
	}
	calls = nil
	if _, err = followDial(ctx, nil, "192.0.2.7:445", log, fakeDial(&calls)); !landiscovery.Unreached(err) || !slices.Equal(calls, []string{"192.0.2.7:445"}) {
		t.Errorf("no follow: %v; calls %q", err, calls)
	}

	// Not found: the first failure, and a warning with the reply count.
	logs.Reset()
	f = newFollow(Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-C"}, none)
	calls = nil
	if _, err = followDial(ctx, f, "192.0.2.7:445", log, fakeDial(&calls)); !landiscovery.Unreached(err) || !slices.Equal(calls, []string{"192.0.2.7:445"}) {
		t.Errorf("not found: %v; calls %q", err, calls)
	}
	if !strings.Contains(logs.String(), `msg="the saved telescope didn't answer, and a scan of this network didn't find it" replies=`) {
		t.Errorf("logs: %s", logs.String())
	}

	// Seen but silent at its new address.
	logs.Reset()
	f = newFollow(Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-B"}, none)
	if _, err = followDial(ctx, f, "192.0.2.7:445", log, fakeDial(&calls)); !landiscovery.Unreached(err) || f.at != "192.0.2.7:445" {
		t.Errorf("seen: %v at %s", err, f.at)
	}
	if !strings.Contains(logs.String(), `didn't answer at its new address" model="Seestar S30"`) {
		t.Errorf("logs: %s", logs.String())
	}

	// An answer from a server is not a moved telescope: no scan.
	saved := lanScanner
	lanScanner = nil
	defer func() { lanScanner = saved }()
	f = newFollow(Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A"}, none)
	refused := errors.New("logon failure")
	if _, err = followDial(ctx, f, "192.0.2.7:445", log, func(context.Context, string, *slog.Logger) (string, error) { return "", refused }); err != refused {
		t.Errorf("a server's answer: %v", err)
	}

	for _, private := range []string{"SERIAL-", "192.0.2.", "S50_A", "S30 B"} {
		if strings.Contains(logs.String(), private) {
			t.Errorf("logs carry %q: %s", private, logs.String())
		}
	}
}

// find with --profile default follows the telescope the app saved on this
// network to its new address, and says so without naming it.
func TestFindFollowsMovedTelescope(t *testing.T) {
	t.Cleanup(readonly.Reset)
	useLAN(t, twoTelescopes())
	useProfile(t, Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A"})
	dev := newDevice()
	dev.set("a.fit", "abc", findDir)
	dev.use(t)
	var calls []string
	device := dial
	dial = func(ctx context.Context, endpoint string, log *slog.Logger) (remote.Session, error) {
		calls = append(calls, endpoint)
		if endpoint != "192.0.2.20:445" {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		}
		return device(ctx, endpoint, log)
	}
	code, out, errs := invoke(t, "find", "--profile", "default")
	if code != exitOK || out != "- 3 2026-10-31T01:00:00Z a.fit\n" || !slices.Equal(calls, []string{"192.0.2.7:445", "192.0.2.20:445"}) {
		t.Fatalf("exit %d out %q calls %q\n%s", code, out, calls, errs)
	}
	if !strings.Contains(errs, `found the saved telescope at a new address on this network" model="Seestar S50"`) ||
		strings.Contains(errs, "192.0.2.20") || strings.Contains(errs, "SERIAL-") {
		t.Errorf("logs: %s", errs)
	}
}
