package ui

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

// lanTCP is the app's TCP dials on fakes: an address in up connects, any
// other is refused. It records each dial as "dial" or "remover" and the
// endpoint.
type lanTCP struct {
	mu    sync.Mutex
	up    []string
	calls []string
	// open is the session a dial that connects returns.
	open func(endpoint string) (remote.Session, error)
	// remover is the remover one that connects returns.
	remover func(endpoint string) (remote.Remover, error)
	// gate, when set, holds a dial until it is closed.
	gate chan struct{}
	// entered, when set, is closed when the first gated dial begins.
	entered     chan struct{}
	enteredOnce sync.Once
}

func (l *lanTCP) reach(kind, endpoint string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, kind+" "+endpoint)
	if !slices.Contains(l.up, endpoint) {
		err := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
		if ap, perr := netip.ParseAddrPort(endpoint); perr == nil {
			err.Addr = net.TCPAddrFromAddrPort(ap)
		}
		return err
	}
	return nil
}

func (l *lanTCP) setUp(up ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.up = up
}

// take returns the dials since the last take.
func (l *lanTCP) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	calls := l.calls
	l.calls = nil
	return calls
}

// useLANTCP makes the app's TCP dials l's for the rest of the test.
func useLANTCP(t *testing.T, l *lanTCP) {
	saved := [...]any{tcpDial, tcpRemover, copyDial}
	t.Cleanup(func() {
		tcpDial = saved[0].(func(context.Context, string, *slog.Logger) (remote.Session, error))
		tcpRemover = saved[1].(func(context.Context, string, *slog.Logger) (remote.Remover, error))
		copyDial = saved[2].(func(context.Context, copyapp.Target) (remote.Session, error))
	})
	copyDial = nil
	if l.open == nil {
		l.open = func(string) (remote.Session, error) {
			return remote.NewFake(fstest.MapFS{"MyWorks/a.jpg": {Data: []byte("a")}}), nil
		}
	}
	if l.remover == nil {
		dir := t.TempDir()
		l.remover = func(string) (remote.Remover, error) { return remote.NewFakeDir(dir) }
	}
	tcpDial = func(ctx context.Context, endpoint string, _ *slog.Logger) (remote.Session, error) {
		if l.gate != nil {
			if l.entered != nil {
				l.enteredOnce.Do(func() { close(l.entered) })
			}
			select {
			case <-l.gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if err := l.reach("dial", endpoint); err != nil {
			return nil, err
		}
		return l.open(endpoint)
	}
	tcpRemover = func(_ context.Context, endpoint string, _ *slog.Logger) (remote.Remover, error) {
		if err := l.reach("remover", endpoint); err != nil {
			return nil, err
		}
		return l.remover(endpoint)
	}
}

// The copier's dials on this network scan for a saved serial every time
// and dial where it answers; a new address is saved. Activity names
// the telescope and both addresses. The remover never scans.
func TestCopierFollowsTelescope(t *testing.T) {
	l := &lanTCP{up: []string{"192.0.2.30:445"}}
	useLANTCP(t, l)
	n := &lanNet{answers: map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.30"): {"SERIAL-A", "S50_A", "Seestar S50"}}}
	useLAN(t, n)
	s := testServer(t)
	s.controller.log = &eventLog{storage: s.storage}
	if err := s.controller.persistRoute(routeLAN, "192.0.2.10:445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	o := copyMountOptions(s, copyapp.MountOptions{})
	stale, err := s.copyTarget()
	if err != nil || stale.Serial != "SERIAL-A" {
		t.Fatalf("target %+v %v", stale, err)
	}
	dial := func(target copyapp.Target) error {
		t.Helper()
		session, err := o.Dial(t.Context(), target)
		if err == nil {
			session.Close()
		}
		return err
	}
	// Moved: only the address that answered is dialed, and it is saved.
	if err := dial(stale); err != nil {
		t.Fatalf("dial: %v", err)
	}
	if calls := l.take(); !slices.Equal(calls, []string{"dial 192.0.2.30:445"}) {
		t.Fatalf("calls %q", calls)
	}
	got, _, _ := loadSettings(s.storage)
	if got.LANAddress != "192.0.2.30:445" || got.LANSerial != "SERIAL-A" {
		t.Fatalf("saved %+v", got)
	}
	if text := trailText(t, s); !strings.Contains(text, `"code":"scan_moved"`) || !strings.Contains(text, "Found S50_A (Seestar S50, serial SERIAL-A) at 192.0.2.30:445, not its saved 192.0.2.10:445, in a scan of this network (1 reply)") {
		t.Fatalf("trail %s", text)
	}
	current, _ := s.copyTarget()
	if current.Endpoint != "192.0.2.30:445" {
		t.Fatalf("current target %+v", current)
	}

	// Every connect scans again, a stale target's included; one already
	// saved isn't recorded again.
	before := trailText(t, s)
	scans := n.scans()
	for _, target := range []copyapp.Target{stale, current} {
		if err := dial(target); err != nil {
			t.Fatal(err)
		}
	}
	if n.scans() != scans+2 || trailText(t, s) != before {
		t.Fatalf("scans %d, trail %s", n.scans()-scans, trailText(t, s))
	}
	l.take()

	// The remover dials the exact target and never scans.
	scans = n.scans()
	if r, err := o.Remover(t.Context(), current); err != nil {
		t.Fatalf("remover: %v", err)
	} else {
		r.Close()
	}
	if _, err := o.Remover(t.Context(), stale); err == nil {
		t.Fatal("a remover at the old address reached the telescope")
	}
	if calls := l.take(); n.scans() != scans || !slices.Equal(calls, []string{"remover 192.0.2.30:445", "remover 192.0.2.10:445"}) {
		t.Fatalf("remover calls %q, scans %d", calls, n.scans()-scans)
	}

	// Without a saved serial nothing scans.
	if err := dial(copyapp.Target{Endpoint: "192.0.2.31:445"}); err == nil || n.scans() != scans {
		t.Fatalf("serial-less dial: %v, scanned %t", err, n.scans() != scans)
	}
	if calls := l.take(); !slices.Equal(calls, []string{"dial 192.0.2.31:445"}) {
		t.Fatalf("serial-less calls %q", calls)
	}

	// Another telescope answers at the saved address: it isn't dialed.
	if err := dial(copyapp.Target{Endpoint: "192.0.2.30:445", Serial: "SERIAL-Z"}); err == nil || l.take() != nil {
		t.Fatalf("another telescope: %v", err)
	}
	if text := trailText(t, s); !strings.Contains(text, `"code":"scan_other_telescope"`) {
		t.Fatalf("trail %s", text)
	}

	// Not seen: the saved address is tried, and Activity says so.
	l.setUp("192.0.2.31:445")
	if err := dial(copyapp.Target{Endpoint: "192.0.2.31:445", Serial: "SERIAL-Z"}); err != nil {
		t.Fatalf("not seen: %v", err)
	}
	if calls := l.take(); !slices.Equal(calls, []string{"dial 192.0.2.31:445"}) {
		t.Fatalf("not seen calls %q", calls)
	}
	if text := trailText(t, s); !strings.Contains(text, `"code":"scan_not_seen"`) {
		t.Fatalf("trail %s", text)
	}

	// Seen, but not connecting where it answered: the saved address isn't
	// tried, and the dial fails.
	l.setUp("192.0.2.10:445")
	if err := dial(stale); err == nil {
		t.Fatal("seen but unreachable connected")
	}
	if calls := l.take(); !slices.Equal(calls, []string{"dial 192.0.2.30:445", "dial 192.0.2.30:4445"}) {
		t.Fatalf("unreached calls %q", calls)
	}
	if text := trailText(t, s); !strings.Contains(text, `"code":"scan_unreached"`) {
		t.Fatalf("trail %s", text)
	}
}

// Dials for one target at once share one scan.
func TestFollowSharesOneScan(t *testing.T) {
	l := &lanTCP{up: []string{"192.0.2.30:445"}, gate: make(chan struct{}), entered: make(chan struct{})}
	useLANTCP(t, l)
	n := &lanNet{answers: map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.30"): {"SERIAL-A", "S50_A", "Seestar S50"}}}
	useLAN(t, n)
	s := testServer(t)
	if err := s.controller.persistRoute(routeLAN, "192.0.2.20:445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	target, _ := s.copyTarget()
	o := copyMountOptions(s, copyapp.MountOptions{})
	var wg sync.WaitGroup
	dial := func(ctx context.Context) {
		wg.Go(func() {
			if session, err := o.Dial(ctx, target); err != nil {
				t.Error(err)
			} else {
				session.Close()
			}
		})
	}
	// The first dial scans and is held in its dial; the other two are
	// let go only once both wait on it.
	dial(t.Context())
	<-l.entered
	for range 2 {
		ctx := &waitingContext{Context: t.Context(), waiting: make(chan struct{})}
		dial(ctx)
		<-ctx.waiting
	}
	close(l.gate)
	wg.Wait()
	if n.scans() != 1 {
		t.Fatalf("simultaneous dials scanned %d times", n.scans())
	}
}

// A moved telescope's new address is saved only over the address and
// serial the search started from, and only if no explicit Save came after
// it started.
func TestPersistMovedChecksIdentity(t *testing.T) {
	s := testServer(t)
	if err := s.controller.persistRoute(routeLAN, "192.0.2.20:445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	gen := s.controller.routeGeneration()
	unchanged := func(what string) {
		t.Helper()
		got, _, _ := loadSettings(s.storage)
		target, _ := s.copyTarget()
		if got.LANAddress != "192.0.2.20:445" || got.LANSerial != "SERIAL-A" || target.Endpoint != "192.0.2.20:445" {
			t.Fatalf("%s: saved %+v target %+v", what, got, target)
		}
	}
	if s.controller.persistMoved("192.0.2.20:445", "192.0.2.30:445", "SERIAL-B", gen) {
		t.Fatal("another serial's address was saved")
	}
	unchanged("another serial")
	if s.controller.persistMoved("192.0.2.20:445", "192.0.2.30:445", "", gen) {
		t.Fatal("a serial-less move was saved")
	}
	unchanged("no serial")
	if s.controller.persistMoved("192.0.2.21:445", "192.0.2.30:445", "SERIAL-A", gen) {
		t.Fatal("a move from another address was saved")
	}
	unchanged("another address")

	// A Save after the search started wins, even of the same route.
	if err := s.controller.persistRoute(routeLAN, "192.0.2.20:445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	if s.controller.persistMoved("192.0.2.20:445", "192.0.2.30:445", "SERIAL-A", gen) {
		t.Fatal("an older search's move overwrote a newer Save")
	}
	unchanged("newer Save")
	if !s.controller.persistMoved("192.0.2.20:445", "192.0.2.30:445", "SERIAL-A", s.controller.routeGeneration()) {
		t.Fatal("a current move wasn't saved")
	}
}

// A probe that hears nothing at an address clears what an earlier scan
// heard there, so a Save of it keeps no serial; and an older scan's result
// never replaces a newer probe's.
func TestSilentProbeClearsSerial(t *testing.T) {
	n := twoTelescopes()
	useLAN(t, n)
	s, _ := copyServer(t)
	scanPost(t, s, `{}`)
	n.set(nil)
	scanPost(t, s, `{"address":"192.0.2.20"}`)
	if err := s.saveRoute(t.Context(), routeLAN, "192.0.2.20"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := loadSettings(s.storage); got.LANAddress != "192.0.2.20:445" || got.LANSerial != "" {
		t.Fatalf("saved %+v", got)
	}

	var l lanState
	older, newer := l.begin(), l.begin()
	a := netip.MustParseAddr("192.0.2.20")
	l.remember(newer, a, landiscovery.ScanResult{})
	l.remember(older, netip.Addr{}, landiscovery.ScanResult{Telescopes: []landiscovery.Telescope{{Serial: "SERIAL-A", Addresses: []netip.Addr{a}}}})
	if serial, ok := l.serial(a); ok {
		t.Fatalf("an older scan replaced a newer probe: %q", serial)
	}
}

// A probe of a typed address doesn't wait behind a scan of the network.
func TestProbeDoesNotWaitForScan(t *testing.T) {
	useLAN(t, twoTelescopes())
	s := testServer(t)
	s.lan.scanMu.Lock()
	defer s.lan.scanMu.Unlock()
	done := make(chan scanView, 1)
	go func() {
		v, _ := s.scan(t.Context(), "192.0.2.40")
		done <- v
	}()
	select {
	case v := <-done:
		if len(v.Telescopes) != 1 {
			t.Fatalf("probe %+v", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the probe waited for the scan")
	}
}

// waitFree polls Free up space until its state is want, or Copy is idle
// when want is "copied", and returns the preview's id and title.
func waitFree(t *testing.T, s *server, want string) (int, string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		w := serve(s, request(s, "GET", "/copy/api/status", ""))
		var v struct {
			Free struct {
				State string `json:"state"`
				ID    int    `json:"id"`
				Title string `json:"title"`
			} `json:"free"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if (want == "copied" && !s.copy.Busy()) || v.Free.State == want {
			return v.Free.ID, v.Free.Title
		}
	}
	w := serve(s, request(s, "GET", "/copy/api/status", ""))
	t.Fatalf("waiting for %s: %s", want, w.Body)
	return 0, ""
}

// Free up space previews where the scan found the telescope and deletes
// only there: once the telescope moves after the preview, the confirmed
// delete fails without a scan and keeps the file, and a new preview finds
// the telescope again. Activity names the address the delete dialed.
func TestFreeSpaceDeletesWherePreviewed(t *testing.T) {
	share := t.TempDir()
	name := filepath.Join(share, "MyWorks", "a.jpg")
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	ancient := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(name, ancient, ancient); err != nil {
		t.Fatal(err)
	}
	l := &lanTCP{
		up:      []string{"192.0.2.30:445"},
		open:    func(string) (remote.Session, error) { return remote.NewFakeDir(share) },
		remover: func(string) (remote.Remover, error) { return remote.NewFakeDir(share) },
	}
	useLANTCP(t, l)
	n := &lanNet{answers: map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.30"): {"SERIAL-A", "S50_A", "Seestar S50"}}}
	useLAN(t, n)
	s := testServer(t)
	s.controller.log = &eventLog{storage: s.storage}
	if err := s.controller.persistRoute(routeLAN, "192.0.2.20:445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	set := copyapp.DefaultSettings()
	set.Folder, set.Once = filepath.Join(t.TempDir(), "copy"), true
	s.copy = copyapp.NewMount(t.Context(), copyMountOptions(s, copyapp.MountOptions{Addr: netip.MustParseAddrPort(s.host), StateDir: t.TempDir(),
		Settings: &set, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(s.copy.Quit)
	post := func(path, body string) int {
		t.Helper()
		return doPost(s, path, body).Code
	}
	if code := post("/copy/api/copy", `{"action":"start"}`); code != 200 {
		t.Fatalf("start = %d", code)
	}
	waitFree(t, s, "copied")
	if code := post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`); code != 200 {
		t.Fatalf("find = %d", code)
	}
	id, _ := waitFree(t, s, "preview")
	if got, _, _ := loadSettings(s.storage); got.LANAddress != "192.0.2.30:445" {
		t.Fatalf("the preview's address wasn't saved: %+v", got)
	}

	// The telescope moves on after the preview.
	n.set(map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.40"): {"SERIAL-A", "S50_A", "Seestar S50"}})
	l.setUp("192.0.2.40:445")
	l.take()
	scans := n.scans()
	post("/copy/api/delete", fmt.Sprintf(`{"action":"delete","id":%d,"folders":["MyWorks"]}`, id))
	waitFree(t, s, "failed")
	if _, err := os.Stat(name); err != nil {
		t.Fatalf("the file was deleted: %v", err)
	}
	if calls := l.take(); n.scans() != scans || len(calls) == 0 || slices.ContainsFunc(calls, func(c string) bool { return c != "remover 192.0.2.30:445" }) {
		t.Fatalf("the confirmed delete dialed %q, scans %d", calls, n.scans()-scans)
	}

	// A new preview finds it, and its delete goes there.
	if code := post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`); code != 200 {
		t.Fatalf("find again = %d", code)
	}
	id, _ = waitFree(t, s, "preview")
	l.take()
	post("/copy/api/delete", fmt.Sprintf(`{"action":"delete","id":%d,"folders":["MyWorks"]}`, id))
	waitFree(t, s, "done")
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatalf("the file wasn't deleted: %v", err)
	}
	if calls := l.take(); !slices.Equal(calls, []string{"remover 192.0.2.40:445"}) {
		t.Fatalf("the delete dialed %q", calls)
	}
	trailText(t, s)
	w := serve(s, request(s, "GET", "/copy/api/status", ""))
	if !strings.Contains(w.Body.String(), "192.0.2.30:445: connect: connection refused") {
		t.Fatalf("Copy's status lacks the address dialed: %s", w.Body)
	}
}

// failedWalk is a session whose listing fails with a TCP error naming
// its server, as an SMB session's errors do.
type failedWalk struct {
	remote.Session
	server string
}

func (f failedWalk) Walk(context.Context, func(string, bool) bool, func(remote.Entry)) ([]remote.Entry, error) {
	addr := net.TCPAddrFromAddrPort(netip.MustParseAddrPort(f.server))
	return nil, fmt.Errorf("walk: %w", &net.OpError{Op: "read", Net: "tcp", Addr: addr, Err: syscall.ECONNRESET})
}

// A copy pass that fails after the scan found the telescope elsewhere
// names the address it connected at in Activity.
func TestFollowedFailureNamesTheAddress(t *testing.T) {
	l := &lanTCP{up: []string{"192.0.2.30:445"}}
	useLANTCP(t, l)
	tcpDial = func(_ context.Context, endpoint string, _ *slog.Logger) (remote.Session, error) {
		if err := l.reach("dial", endpoint); err != nil {
			return nil, err
		}
		return failedWalk{remote.NewFake(fstest.MapFS{}), endpoint}, nil
	}
	useLAN(t, &lanNet{answers: map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.30"): {"SERIAL-A", "S50_A", "Seestar S50"}}})
	s := testServer(t)
	s.controller.log = &eventLog{storage: s.storage}
	if err := s.controller.persistRoute(routeLAN, "192.0.2.20:445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	set := copyapp.DefaultSettings()
	set.Folder, set.Once = filepath.Join(t.TempDir(), "copy"), true
	s.copy = copyapp.NewMount(t.Context(), copyMountOptions(s, copyapp.MountOptions{Addr: netip.MustParseAddrPort(s.host), StateDir: t.TempDir(),
		Settings: &set, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(s.copy.Quit)
	if w := doPost(s, "/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	waitFree(t, s, "copied")
	w := serve(s, request(s, "GET", "/copy/api/status", ""))
	var v struct {
		Activity []struct {
			Text string `json:"text"`
		} `json:"activity"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	failed := false
	for _, e := range v.Activity {
		failed = failed || strings.Contains(e.Text, "192.0.2.30:445: connection reset")
	}
	if !failed {
		t.Errorf("no failed pass in Activity: %s", w.Body)
	}
	trailText(t, s)
}

// waitingContext closes waiting the first time its Done is called, which
// for a dial that finds a search for its target running is when it
// begins to wait on that search.
type waitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

// probe is a connection a test's dial returns.
type probe string

func (probe) Close() error { return nil }

// Dials that wait on another's search for the same target don't save its
// address over an explicit Save made meanwhile, even of the same settings.
func TestConcurrentConnectsRespectRouteSave(t *testing.T) {
	n := &lanNet{answers: map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.30"): {"SERIAL-A", "S50_A", "Seestar S50"}}}
	useLAN(t, n)
	s := testServer(t)
	if err := s.controller.persistRoute(routeLAN, "192.0.2.20:445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	target, _ := s.copyTarget()
	gen := s.controller.routeGeneration()
	gate, started := make(chan struct{}), make(chan struct{})
	var first sync.Once
	var mu sync.Mutex
	calls := 0
	dial := func(ctx context.Context, endpoint string) (probe, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		first.Do(func() { close(started) })
		select {
		case <-gate:
			return probe(endpoint), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	var wg sync.WaitGroup
	run := func(ctx context.Context) {
		wg.Go(func() {
			got, at, err := lanFollow(ctx, s, target, gen, dial)
			if err != nil || got != "192.0.2.30:445" || at.Endpoint != string(got) {
				t.Errorf("dialed %q at %q: %v", got, at.Endpoint, err)
			}
		})
	}
	run(t.Context())
	<-started
	for range 2 {
		ctx := &waitingContext{Context: t.Context(), waiting: make(chan struct{})}
		run(ctx)
		<-ctx.waiting
	}
	if err := s.controller.persistRoute(routeLAN, "192.0.2.20:445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	close(gate)
	wg.Wait()
	got, _, _ := loadSettings(s.storage)
	now, _ := s.copyTarget()
	if got.LANAddress != "192.0.2.20:445" || now.Endpoint != got.LANAddress || calls != 3 || n.scans() != 1 {
		t.Fatalf("saved %s, target %s, dials %d, scans %d", got.LANAddress, now.Endpoint, calls, n.scans())
	}
}

// A dial that waited on a search from before an explicit Save, and
// searches on its own when the address that search found fails, saves
// nothing over the Save: its search is under the save its connect read.
func TestWaiterSearchKeepsItsGeneration(t *testing.T) {
	n := &lanNet{answers: map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.30"): {"SERIAL-A", "S50_A", "Seestar S50"}}}
	useLAN(t, n)
	s := testServer(t)
	s.controller.log = &eventLog{storage: s.storage}
	if err := s.controller.persistRoute(routeLAN, "192.0.2.20:445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	target, _ := s.copyTarget()
	gen := s.controller.routeGeneration()
	gate, started := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := 0
	dial := func(ctx context.Context, endpoint string) (probe, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		switch call {
		case 1: // the first search's, held until the Save
			close(started)
			select {
			case <-gate:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		case 2: // the waiter's, where that search connected
			return "", errors.New("refused")
		}
		return probe(endpoint), nil
	}
	first := make(chan error, 1)
	go func() {
		_, _, err := lanFollow(t.Context(), s, target, gen, dial)
		first <- err
	}()
	<-started
	waiting := &waitingContext{Context: t.Context(), waiting: make(chan struct{})}
	second := make(chan error, 1)
	go func() {
		_, _, err := lanFollow(waiting, s, target, gen, dial)
		second <- err
	}()
	<-waiting.waiting
	if err := s.controller.persistRoute(routeLAN, "192.0.2.20:445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	close(gate)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if got, _, _ := loadSettings(s.storage); got.LANAddress != "192.0.2.20:445" || calls != 3 || n.scans() != 2 {
		t.Fatalf("saved %s, dials %d, scans %d", got.LANAddress, calls, n.scans())
	}
	if text := trailText(t, s); strings.Contains(text, `"code":"scan_moved"`) {
		t.Errorf("trail: %s", text)
	}
}

// Two telescopes saved at one address, under one save, don't share a
// search: a dial for the second serial, made while the first's search is
// still connecting, scans for its own and reaches it where it answers.
func TestFollowKeysSerial(t *testing.T) {
	s, _, n := scanModeServer(t, "connect")
	n.set(map[netip.Addr][3]string{
		netip.MustParseAddr("192.0.2.30"): {"SERIAL-A", "S50_A", "Seestar S50"},
		netip.MustParseAddr("192.0.2.40"): {"SERIAL-B", "S50_B", "Seestar S50"},
	})
	a, _ := s.copyTarget()
	b := a
	b.Serial = "SERIAL-B"
	gen := s.controller.routeGeneration()
	gate, entered := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(gate) })
	first := make(chan error, 1)
	go func() {
		_, _, err := lanFollow(t.Context(), s, a, gen, func(_ context.Context, endpoint string) (probe, error) {
			close(entered)
			<-gate
			return probe(endpoint), nil
		})
		first <- err
	}()
	<-entered
	waiting := &waitingContext{Context: t.Context(), waiting: make(chan struct{})}
	second := make(chan copyapp.Target, 1)
	go func() {
		_, at, err := lanFollow(waiting, s, b, gen, func(_ context.Context, endpoint string) (probe, error) { return probe(endpoint), nil })
		if err != nil {
			t.Error(err)
		}
		second <- at
	}()
	<-waiting.waiting
	once.Do(func() { close(gate) })
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if at := <-second; at.Endpoint != "192.0.2.40:445" || n.scans() != 2 {
		t.Fatalf("the second serial reached %q, scans %d", at.Endpoint, n.scans())
	}
}

// Another serial answering at the saved address while the saved one
// answers elsewhere and doesn't connect there is recorded as a sighting
// that didn't connect, not as another telescope in its place.
func TestConflictWithSightingIsRecordedAsUnreached(t *testing.T) {
	useLAN(t, &lanNet{answers: map[netip.Addr][3]string{
		netip.MustParseAddr("192.0.2.20"): {"SERIAL-B", "Other", "Seestar S30"},
		netip.MustParseAddr("192.0.2.30"): {"SERIAL-A", "S50_A", "Seestar S50"},
	}})
	s := testServer(t)
	s.controller.log = &eventLog{storage: s.storage}
	if err := s.controller.persistRoute(routeLAN, "192.0.2.20:445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	target, _ := s.copyTarget()
	var dialed []string
	_, _, err := lanSearch(t.Context(), s, target, s.controller.routeGeneration(), func(_ context.Context, endpoint string) (probe, error) {
		dialed = append(dialed, endpoint)
		return "", errors.New("refused")
	})
	if err == nil || !slices.Equal(dialed, []string{"192.0.2.30:445", "192.0.2.30:4445"}) {
		t.Fatalf("err %v, dialed %q", err, dialed)
	}
	if text := trailText(t, s); !strings.Contains(text, `"code":"scan_unreached"`) || strings.Contains(text, "scan_other_telescope") ||
		!strings.Contains(text, "Found S50_A (Seestar S50, serial SERIAL-A) on this network (6 replies), but it didn't connect where it answered, at 192.0.2.30.") {
		t.Errorf("trail: %s", text)
	}
}
