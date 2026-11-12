package cli

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
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
	"testing/fstest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
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
	listens int // sockets opened: one per scan
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
			n.mu.Lock()
			n.listens++
			n.mu.Unlock()
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

func (c *lanConn) WriteTo(b []byte, _ *ipv4.ControlMessage, to net.Addr) (int, error) {
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
// that neither stream carries the password or its hash.
func scanRun(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Cleanup(readonly.Reset)
	code, stdout, stderr = invoke(t, append([]string{"scan"}, args...)...)
	for _, secret := range []string{"password", "0123456789abcdef"} {
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
	want := []string{`192.0.2.20 busy S50_A "Seestar S50" SERIAL-A`, `192.0.2.40 busy "S30 B" "Seestar S30" SERIAL-B`}
	if code != exitOK || !slices.Equal(lines, want) {
		t.Errorf("exit %d lines %q, want %q\n%s", code, lines, want, errs)
	}
	if !strings.Contains(errs, `msg="scanned this network" replies=6 telescopes=2`) ||
		!strings.Contains(errs, `msg="found a telescope" telescope="S50_A (Seestar S50, serial SERIAL-A)" addresses=[192.0.2.20]`) {
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
	if got.Replies != 6 || len(got.Telescopes) != 2 || got.Telescopes[0].Name != "S30 B" || got.Telescopes[0].Model != "Seestar S30" || got.Telescopes[0].Serial != "SERIAL-B" ||
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

// Every send failing otherwise is a failed scan whose error names the
// address it sent to.
func TestScanSendFails(t *testing.T) {
	useLAN(t, &lanNet{sendErr: &net.OpError{Op: "write", Net: "udp", Addr: net.UDPAddrFromAddrPort(netip.MustParseAddrPort("192.0.2.255:4720")),
		Err: os.NewSyscallError("sendto", syscall.ENOBUFS)}})
	code, out, errs := scanRun(t)
	if code != exitFailure || out != "" || !strings.Contains(errs, "LAN scan: write udp 192.0.2.255:4720") {
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
	for _, want := range []string{"UDP port 4720", "--format json", "the serial", "Local Network", "--endpoint ADDRESS:445"} {
		if code != exitOK || !strings.Contains(out, want) {
			t.Errorf("help lacks %q: exit %d\n%s", want, code, out)
		}
	}
}

func (n *lanNet) scans() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.listens
}

// A run follows the telescope only at the endpoint the profile filled, and
// only with a saved serial; delete pins the address it first connects at.
func TestNewFollow(t *testing.T) {
	s := Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A"}
	if f := newFollow(s, none); f == nil || f.saved != s.Endpoint || f.serial != "SERIAL-A" || f.pin {
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
	if fo.follow == nil || so.follow != nil || bo.follow == nil || do.follow == nil || fo.follow.pin || bo.follow.pin || !do.follow.pin {
		t.Errorf("fillSaved follow: find %+v sync %+v browse %+v delete %+v", fo.follow, so.follow, bo.follow, do.follow)
	}
}

// fakeSession is what fakeDial connects: where it connected.
type fakeSession string

func (fakeSession) Close() error { return nil }

// fakeDial answers at the addresses in up and gets no answer elsewhere.
func fakeDial(calls *[]string, up ...string) func(context.Context, string, *slog.Logger) (fakeSession, error) {
	return func(_ context.Context, endpoint string, _ *slog.Logger) (fakeSession, error) {
		*calls = append(*calls, endpoint)
		if slices.Contains(up, endpoint) {
			return fakeSession("session at " + endpoint), nil
		}
		return "", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	}
}

func TestFollowDial(t *testing.T) {
	ctx := context.Background()
	n := twoTelescopes()
	useLAN(t, n)
	var logs bytes.Buffer
	log := diagnostic.Logger("text", &logs, slog.LevelInfo)
	// connect mode: every connect scans.
	f := newFollow(Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A", LANScan: "connect"}, none)
	var calls []string
	follow := func(f *follow, endpoint string, up ...string) (fakeSession, error) {
		calls = nil
		return followDial(ctx, f, endpoint, log, fakeDial(&calls, up...))
	}

	// Moved: the scan finds the serial elsewhere, and the run connects
	// there without trying the saved address.
	got, err := follow(f, "192.0.2.7:445", "192.0.2.7:445", "192.0.2.20:445")
	if err != nil || got != "session at 192.0.2.20:445" || !slices.Equal(calls, []string{"192.0.2.20:445"}) {
		t.Fatalf("moved = %q, %v; calls %q", got, err, calls)
	}
	if !strings.Contains(logs.String(), `level=INFO msg="found the telescope at another address on this network" telescope="S50_A (Seestar S50, serial SERIAL-A)" saved=192.0.2.7:445 endpoint=192.0.2.20:445 addresses=[192.0.2.20] replies=`) {
		t.Errorf("logs: %s", logs.String())
	}
	// In connect mode every connect scans again, and a repeated outcome
	// isn't logged again.
	logs.Reset()
	if got, err = follow(f, "192.0.2.7:445", "192.0.2.20:445"); err != nil || !slices.Equal(calls, []string{"192.0.2.20:445"}) || n.scans() != 2 {
		t.Errorf("the next dial = %q, %v; calls %q scans %d", got, err, calls, n.scans())
	}
	if logs.Len() != 0 {
		t.Errorf("repeat logged: %s", logs.String())
	}

	// The saved address answering with the serial is dialed, and isn't a move.
	f = newFollow(Saved{Endpoint: "192.0.2.20:445", LANSerial: "SERIAL-A"}, none)
	if got, err = follow(f, "192.0.2.20:445", "192.0.2.20:445"); err != nil || !slices.Equal(calls, []string{"192.0.2.20:445"}) || logs.Len() != 0 {
		t.Errorf("at the saved address = %q, %v; calls %q logs %s", got, err, calls, logs.String())
	}

	// Another endpoint, or no follow, is dialed as it is, unscanned.
	scans := n.scans()
	if _, err = follow(f, "198.51.100.9:445"); err == nil || !slices.Equal(calls, []string{"198.51.100.9:445"}) {
		t.Errorf("another endpoint: %v; calls %q", err, calls)
	}
	if _, err = follow(nil, "192.0.2.20:445"); err == nil || !slices.Equal(calls, []string{"192.0.2.20:445"}) {
		t.Errorf("no follow: %v; calls %q", err, calls)
	}
	if n.scans() != scans {
		t.Errorf("an unfollowed dial scanned")
	}

	// Not seen: the saved address, and a warning that names the serial.
	logs.Reset()
	f = newFollow(Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-C"}, none)
	if got, err = follow(f, "192.0.2.7:445", "192.0.2.7:445"); err != nil || !slices.Equal(calls, []string{"192.0.2.7:445"}) {
		t.Errorf("not seen: %q, %v; calls %q", got, err, calls)
	}
	if !strings.Contains(logs.String(), `level=WARN msg="the telescope wasn't seen in a scan of this network; tried its saved address" serial=SERIAL-C saved=192.0.2.7:445 replies=`) {
		t.Errorf("logs: %s", logs.String())
	}

	// Another telescope at the saved address: it is never dialed.
	logs.Reset()
	f = newFollow(Saved{Endpoint: "192.0.2.20:445", LANSerial: "SERIAL-C"}, none)
	if _, err = follow(f, "192.0.2.20:445", "192.0.2.20:445"); !errors.Is(err, landiscovery.ErrOtherTelescope) || calls != nil {
		t.Errorf("other telescope: %v; calls %q", err, calls)
	}
	if !strings.Contains(logs.String(), `msg="another telescope answers at the saved address, and a scan of this network didn't find the saved one" serial=SERIAL-C saved=192.0.2.20:445 other=`) {
		t.Errorf("logs: %s", logs.String())
	}

	// Seen but not connecting where it answered: the saved address isn't
	// tried, and the error is the dial's.
	logs.Reset()
	f = newFollow(Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-B"}, none)
	if _, err = follow(f, "192.0.2.7:445", "192.0.2.7:445"); !errors.Is(err, syscall.ECONNREFUSED) || !slices.Equal(calls, []string{"192.0.2.40:445"}) {
		t.Errorf("seen: %v; calls %q", err, calls)
	}
	if !strings.Contains(logs.String(), `didn't connect where it answered" telescope="S30 B (Seestar S30, serial SERIAL-B)" saved=192.0.2.7:445 addresses=[192.0.2.40] replies=`) {
		t.Errorf("logs: %s", logs.String())
	}

	// A pinned follow keeps the first address that connected, unscanned,
	// in every mode that scans, even when a connect there fails.
	n.mu.Lock()
	answers := n.answers
	n.mu.Unlock()
	for _, mode := range []string{"start", "connect"} {
		n.mu.Lock()
		n.answers = answers
		n.mu.Unlock()
		f = newFollow(Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A", LANScan: mode}, none)
		f.pin = true
		if _, err = follow(f, "192.0.2.7:445", "192.0.2.20:445"); err != nil {
			t.Fatal(err)
		}
		scans = n.scans()
		if _, err = follow(f, "192.0.2.7:445", "192.0.2.20:445"); err != nil || !slices.Equal(calls, []string{"192.0.2.20:445"}) || n.scans() != scans {
			t.Errorf("%s pinned: %v; calls %q scans %d, want %d", mode, err, calls, n.scans(), scans)
		}
		if _, err = follow(f, "192.0.2.7:445"); err == nil || !slices.Equal(calls, []string{"192.0.2.20:445"}) || n.scans() != scans {
			t.Errorf("%s pinned, refused: %v; calls %q scans %d, want %d", mode, err, calls, n.scans(), scans)
		}
	}
}

// find with --profile default follows the telescope the app saved on this
// network to its new address, and says so, naming it and both addresses.
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
	if code != exitOK || out != "- 3 2026-10-31T01:00:00Z a.fit\n" || !slices.Equal(calls, []string{"192.0.2.20:445"}) {
		t.Fatalf("exit %d out %q calls %q\n%s", code, out, calls, errs)
	}
	if !strings.Contains(errs, `found the telescope at another address on this network" telescope="S50_A (Seestar S50, serial SERIAL-A)" saved=192.0.2.7:445 endpoint=192.0.2.20:445`) {
		t.Errorf("logs: %s", errs)
	}
}

// delete with --profile default lists and deletes through one session at
// the address the scan found; a dry run lists through a read-only one the
// same way.
func TestDeleteFollowsTelescope(t *testing.T) {
	n := twoTelescopes()
	useLAN(t, n)
	useProfile(t, Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A"})
	s, _ := modesShare(t)
	s.use(t)
	useDial(t)
	open, openRemover := dial, dialRemover
	var calls []string
	dial = func(ctx context.Context, endpoint string, log *slog.Logger) (remote.Session, error) {
		calls = append(calls, "dial "+endpoint)
		return open(ctx, endpoint, log)
	}
	dialRemover = func(ctx context.Context, endpoint string, log *slog.Logger) (remote.Remover, error) {
		calls = append(calls, "remover "+endpoint)
		return openRemover(ctx, endpoint, log)
	}
	t.Cleanup(readonly.Reset)
	for _, tc := range []struct {
		force bool
		want  string
	}{{false, "dial 192.0.2.20:445"}, {true, "remover 192.0.2.20:445"}} {
		readonly.Reset()
		calls = nil
		scans := n.scans()
		args := []string{"delete", "--profile", "default", "--ignore-state", "--include", "*.fit"}
		if tc.force {
			args = append(args, "--force")
		}
		code, _, errs := invoke(t, args...)
		if code != exitOK || n.scans() != scans+1 || !slices.Equal(calls, []string{tc.want}) {
			t.Errorf("force %t: exit %d scans %d calls %q\n%s", tc.force, code, n.scans()-scans, calls, errs)
		}
		if !strings.Contains(errs, "192.0.2.20:445") || !strings.Contains(errs, "SERIAL-A") {
			t.Errorf("logs: %s", errs)
		}
	}
	if !s.has(t, "MyWorks/M 31/f.fit") || s.has(t, "MyWorks/M 31/a.fit") {
		t.Errorf("deleted the wrong files")
	}
}

// useDial restores dial and dialRemover after the test.
func useDial(t *testing.T) {
	saved, savedRemover := dial, dialRemover
	t.Cleanup(func() { dial, dialRemover = saved, savedRemover })
}

// failedWalk is a session whose listing fails naming its server, as an
// SMB session's errors do.
type failedWalk struct {
	remote.Session
	server string
}

func (f failedWalk) Walk(context.Context, func(string, bool) bool, func(remote.Entry)) ([]remote.Entry, error) {
	return nil, fmt.Errorf("walk on %s: connection reset", f.server)
}

// A listing that fails after the scan found the telescope elsewhere names
// the address it connected at.
func TestFollowedFailureNamesTheAddress(t *testing.T) {
	useLAN(t, twoTelescopes())
	useDial(t)
	dial = func(_ context.Context, endpoint string, _ *slog.Logger) (remote.Session, error) {
		if endpoint != "192.0.2.20:445" {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		}
		return failedWalk{remote.NewFake(fstest.MapFS{}), endpoint}, nil
	}
	f := newFollow(Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A"}, none)
	var logs bytes.Buffer
	err := list(t.Context(), findOptions{endpoint: "192.0.2.7:445", follow: f}, nil, io.Discard, diagnostic.Logger("text", &logs, slog.LevelDebug))
	if err == nil || !strings.Contains(err.Error(), "192.0.2.20:445: connection reset") {
		t.Fatalf("list = %v", err)
	}
	if !strings.Contains(logs.String(), "endpoint=192.0.2.20:445") {
		t.Fatalf("logs: %s", logs.String())
	}
}
