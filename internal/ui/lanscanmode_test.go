package ui

import (
	"context"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// scanModeServer is a server saved on this network at 192.0.2.10 with
// SERIAL-A and lan_scan mode, whose telescope answers at 192.0.2.30 and
// holds MyWorks/a.jpg.
func scanModeServer(t *testing.T, mode string) (*server, *lanTCP, *lanNet) {
	t.Helper()
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
	if err := s.controller.persistRoute(routeLAN, "192.0.2.10:445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	if mode != "" {
		if w := doPost(s, "/api/lan-scan", `{"settings":{"lan_scan":"`+mode+`"}}`); w.Code != 200 {
			t.Fatalf("lan-scan %s = %d %s", mode, w.Code, w.Body)
		}
	}
	return s, l, n
}

// modeDials dials the current target count times with o.Dial in ctx and
// returns the scans that took.
func modeDials(t *testing.T, s *server, n *lanNet, ctx context.Context, count int) int {
	t.Helper()
	o := copyMountOptions(s, copyapp.MountOptions{})
	before := n.scans()
	for range count {
		target, err := s.copyTarget()
		if err != nil {
			t.Fatal(err)
		}
		session, err := o.Dial(ctx, target)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		session.Close()
	}
	return n.scans() - before
}

// In start mode a run's connects scan once, the first, and dial where it
// connected after that; a connect outside a run scans each time.
func TestStartModeScansOncePerRun(t *testing.T) {
	for _, mode := range []string{"", "start"} {
		t.Run("mode "+mode, func(t *testing.T) {
			s, l, n := scanModeServer(t, mode)
			ctx := landiscovery.WithRun(t.Context(), &landiscovery.Run{})
			if got := modeDials(t, s, n, ctx, 3); got != 1 {
				t.Fatalf("a run of three connects scanned %d times", got)
			}
			if calls := l.take(); !slices.Equal(calls, slices.Repeat([]string{"dial-as " + lanName + " 192.0.2.30:445"}, 3)) {
				t.Fatalf("calls %q", calls)
			}
			// The scan saved the new address; the run keeps it.
			if got, _ := loadSettings(s.storage); got.LANAddress != "192.0.2.30:445" {
				t.Fatalf("saved %+v", got)
			}
			// Another run scans again, once.
			if got := modeDials(t, s, n, landiscovery.WithRun(t.Context(), &landiscovery.Run{}), 2); got != 1 {
				t.Fatalf("a second run scanned %d times", got)
			}
			// Outside a run every connect scans.
			if got := modeDials(t, s, n, t.Context(), 2); got != 2 {
				t.Fatalf("connects outside a run scanned %d times", got)
			}
			trailText(t, s)
		})
	}
}

// In start mode a connect that fails where the run's scan found the
// telescope scans again, once, with the same rules: it follows the
// serial, and another telescope at the address it found is not used.
func TestStartModeRescansAfterAFailedConnect(t *testing.T) {
	s, l, n := scanModeServer(t, "start")
	o := copyMountOptions(s, copyapp.MountOptions{})
	ctx := landiscovery.WithRun(t.Context(), &landiscovery.Run{})
	dial := func() error {
		t.Helper()
		target, err := s.copyTarget()
		if err != nil {
			t.Fatal(err)
		}
		session, err := o.Dial(ctx, target)
		if err == nil {
			session.Close()
		}
		return err
	}
	if err := dial(); err != nil || n.scans() != 1 {
		t.Fatalf("first connect: %v, %d scans", err, n.scans())
	}
	l.take()

	// The telescope moves: the reuse fails, and one scan finds it.
	n.set(map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.40"): {"SERIAL-A", "S50_A", "Seestar S50"}})
	l.setUp("192.0.2.40:445")
	if err := dial(); err != nil || n.scans() != 2 {
		t.Fatalf("moved: %v, %d scans", err, n.scans())
	}
	if calls := l.take(); !slices.Equal(calls, []string{"dial-as " + lanName + " 192.0.2.30:445", "dial-as " + lanName + " 192.0.2.40:445"}) {
		t.Fatalf("moved calls %q", calls)
	}
	if err := dial(); err != nil || n.scans() != 2 {
		t.Fatalf("after the move: %v, %d scans", err, n.scans())
	}
	l.take()

	// Another telescope takes its address: the rescan blocks it.
	n.set(map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.40"): {"SERIAL-B", "Other", "Seestar S30"}})
	l.setUp()
	if err := dial(); err == nil || n.scans() != 3 {
		t.Fatalf("other telescope: %v, %d scans", err, n.scans())
	}
	if calls := l.take(); !slices.Equal(calls, []string{"dial-as " + lanName + " 192.0.2.40:445"}) {
		t.Fatalf("other telescope calls %q", calls)
	}
	// A failed search keeps no address: the next connect scans again
	// rather than dialing the blocked one.
	l.setUp("192.0.2.40:445")
	if err := dial(); err == nil || n.scans() != 4 || l.take() != nil {
		t.Fatalf("after the block: %v, %d scans", err, n.scans())
	}
	if text := trailText(t, s); !strings.Contains(text, `"code":"scan_other_telescope"`) || !strings.Contains(text, `"code":"scan_moved"`) {
		t.Fatalf("trail %s", text)
	}
}

// In connect mode every connect scans, in a run or not.
func TestConnectModeScansEachConnect(t *testing.T) {
	s, _, n := scanModeServer(t, "connect")
	if got := modeDials(t, s, n, landiscovery.WithRun(t.Context(), &landiscovery.Run{}), 3); got != 3 {
		t.Fatalf("a run of three connects scanned %d times", got)
	}
}

// In off mode a connect dials the saved address alone, records nothing,
// and a typed address gets no probe; Scan again still scans.
func TestOffModeNeverScansUnasked(t *testing.T) {
	s, l, n := scanModeServer(t, "off")
	l.setUp("192.0.2.10:445")
	for _, ctx := range []context.Context{t.Context(), landiscovery.WithRun(t.Context(), &landiscovery.Run{})} {
		if got := modeDials(t, s, n, ctx, 2); got != 0 {
			t.Fatalf("off scanned %d times", got)
		}
	}
	if calls := l.take(); !slices.Equal(calls, slices.Repeat([]string{"dial-as " + lanName + " 192.0.2.10:445"}, 4)) {
		t.Fatalf("calls %q", calls)
	}
	// The saved address doesn't connect: still no scan.
	l.setUp()
	target, _ := s.copyTarget()
	if _, err := copyMountOptions(s, copyapp.MountOptions{}).Dial(t.Context(), target); err == nil || n.scans() != 0 {
		t.Fatalf("off, unreachable: %v, %d scans", err, n.scans())
	}
	if text := trailText(t, s); strings.Contains(text, `"code":"scan_`) {
		t.Fatalf("off recorded a scan: %s", text)
	}
	// Test's probe of a typed address sends nothing.
	if v := scanPost(t, s, `{"address":"192.0.2.30"}`); len(v.Telescopes) != 0 || n.scans() != 0 || len(n.sends()) != 0 {
		t.Fatalf("off probed: %+v, %d sockets, sent %v", v, n.scans(), n.sends())
	}
	// Scan again, pressed, scans.
	if v := scanPost(t, s, `{}`); len(v.Telescopes) != 1 || n.scans() != 1 {
		t.Fatalf("Scan again: %+v, %d sockets", v, n.scans())
	}
}

// /api/lan-scan saves the mode alone; a value it doesn't know is refused,
// and a change is refused while connected through the account. The
// file's lan_scan survives other saves, and one this version doesn't
// know reads as start and is kept until changed.
func TestLANScanSetting(t *testing.T) {
	s, _, _ := scanModeServer(t, "")
	if got, _ := loadSettings(s.storage); got.LANScan != "" || s.lanScan() != landiscovery.ScanStart {
		t.Fatalf("default %q, %s", got.LANScan, s.lanScan())
	}
	for _, mode := range []string{"off", "connect", "start"} {
		if w := doPost(s, "/api/lan-scan", `{"settings":{"lan_scan":"`+mode+`"}}`); w.Code != 200 {
			t.Fatalf("%s = %d %s", mode, w.Code, w.Body)
		}
		got, _ := loadSettings(s.storage)
		if got.LANScan != mode || string(s.lanScan()) != mode || got.LANAddress != "192.0.2.10:445" || got.LANSerial != "SERIAL-A" || got.Route != routeLAN {
			t.Fatalf("%s saved %+v", mode, got)
		}
	}
	for _, bad := range []string{"", "never", "OFF", `1`} {
		body := `{"settings":{"lan_scan":"` + bad + `"}}`
		if bad == "1" {
			body = `{"settings":{"lan_scan":1}}`
		}
		if w := doPost(s, "/api/lan-scan", body); w.Code != 400 {
			t.Fatalf("%q = %d %s", bad, w.Code, w.Body)
		}
	}
	if got, _ := loadSettings(s.storage); got.LANScan != "start" {
		t.Fatalf("a refused value changed it: %+v", got)
	}

	// A route save keeps it.
	doPost(s, "/api/lan-scan", `{"settings":{"lan_scan":"off"}}`)
	if err := s.saveRoute(t.Context(), routeLAN, "192.0.2.11"); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadSettings(s.storage); got.LANScan != "off" {
		t.Fatalf("a route save dropped it: %+v", got)
	}

	// A newer version's value reads as start and is kept.
	b, err := json.Marshal(map[string]any{"version": 1, "route": "lan", "lan_address": "192.0.2.10:445", "lan_serial": "SERIAL-A", "lan_scan": "a newer mode"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.storage.writeBytes("config.json", b); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(s.storage)
	if err != nil {
		t.Fatal(err)
	}
	if landiscovery.ModeOf(got.LANScan) != landiscovery.ScanStart {
		t.Fatalf("%q reads as %s", got.LANScan, landiscovery.ModeOf(got.LANScan))
	}
	s.settings, s.controller.lastSaved = got, got
	if err := s.saveRoute(t.Context(), routeLAN, "192.0.2.12"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(s.storage.root, "config.json"))
	if !strings.Contains(string(raw), `"lan_scan":"a newer mode"`) {
		t.Fatalf("a newer value wasn't kept: %s", raw)
	}
}

// Copying is one run: Start scans once for all its passes, even after
// the scan found the telescope at a new address, and a new Start scans
// again.
func TestCopyStartScansOnce(t *testing.T) {
	s, l, n := scanModeServer(t, "")
	set := copyapp.DefaultSettings()
	set.Folder = filepath.Join(t.TempDir(), "copy")
	s.copy = copyapp.NewMount(t.Context(), copyMountOptions(s, copyapp.MountOptions{Addr: netip.MustParseAddrPort(s.host), StateDir: t.TempDir(),
		Settings: &set, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(s.copy.Quit)
	dials := func() int {
		l.mu.Lock()
		defer l.mu.Unlock()
		return len(l.calls)
	}
	waitDials := func(want int) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); dials() < want; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("waiting for %d dials, have %q", want, l.take())
			}
		}
	}
	waitIdle := func() {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
			if strings.Contains(serve(s, request(s, "GET", "/copy/api/status", "")).Body.String(), `"state":"uptodate"`) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("copying never caught up: %s", serve(s, request(s, "GET", "/copy/api/status", "")).Body)
			}
		}
	}
	if w := doPost(s, "/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	waitDials(1)
	waitIdle()
	have := dials()
	for range 2 {
		if w := doPost(s, "/copy/api/copy", `{"action":"check"}`); w.Code != 200 {
			t.Fatalf("check = %d %s", w.Code, w.Body)
		}
		have++
		waitDials(have)
		waitIdle()
	}
	if n.scans() != 1 {
		t.Fatalf("three passes scanned %d times", n.scans())
	}
	if calls := l.take(); slices.ContainsFunc(calls, func(c string) bool { return c != "dial-as "+lanName+" 192.0.2.30:445" }) {
		t.Fatalf("calls %q", calls)
	}
	doPost(s, "/copy/api/copy", `{"action":"stop"}`)
	for deadline := time.Now().Add(10 * time.Second); s.copy.Busy(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("copying never stopped")
		}
	}
	if w := doPost(s, "/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
		t.Fatalf("start again = %d %s", w.Code, w.Body)
	}
	waitDials(1)
	waitIdle()
	if n.scans() != 2 {
		t.Fatalf("a new Start scanned %d times in all, want 2", n.scans())
	}
	doPost(s, "/copy/api/copy", `{"action":"stop"}`)
	trailText(t, s)
}

// A Free up space check and a Copy preview are each a run, and Browse is
// one from each time its page loads.
func TestPreviewsAndBrowseAreRuns(t *testing.T) {
	s, _, n := scanModeServer(t, "")
	set := copyapp.DefaultSettings()
	set.Folder, set.Once = filepath.Join(t.TempDir(), "copy"), true
	s.copy = copyapp.NewMount(t.Context(), copyMountOptions(s, copyapp.MountOptions{Addr: netip.MustParseAddrPort(s.host), StateDir: t.TempDir(),
		Settings: &set, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(s.copy.Quit)
	if code := doPost(s, "/copy/api/copy", `{"action":"start"}`).Code; code != 200 {
		t.Fatalf("start = %d", code)
	}
	waitFree(t, s, "copied")
	if n.scans() != 1 {
		t.Fatalf("a Copy start scanned %d times", n.scans())
	}

	if code := doPost(s, "/copy/api/delete", `{"action":"find","days":"0","trust":false}`).Code; code != 200 {
		t.Fatalf("find = %d", code)
	}
	waitFree(t, s, "preview")
	if n.scans() != 2 {
		t.Fatalf("a Free up space check scanned %d times", n.scans()-1)
	}
	if code := doPost(s, "/copy/api/delete", `{"action":"find","days":"0","trust":false}`).Code; code != 200 {
		t.Fatalf("find again = %d", code)
	}
	waitFree(t, s, "preview")
	if n.scans() != 3 {
		t.Fatalf("a second check scanned %d times", n.scans()-2)
	}
	// A Copy preview is a run of its own.
	if code := doPost(s, "/copy/api/copy", `{"action":"preview"}`).Code; code != 200 {
		t.Fatalf("preview = %d", code)
	}
	for deadline := time.Now().Add(10 * time.Second); n.scans() < 4; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the Copy preview never scanned")
		}
	}

	browse := func(path string) {
		t.Helper()
		r := httptest.NewRequest("GET", "http://"+s.host+path, nil)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if w := serve(s, r); w.Code != 200 {
			t.Fatalf("%s = %d %s", path, w.Code, w.Body)
		}
	}
	browse("/browse/")
	scans := n.scans()
	browse("/browse/api/list")
	if n.scans() != scans+1 {
		t.Fatalf("Browse's first listing scanned %d times", n.scans()-scans)
	}
	trailText(t, s)
}

// The mode can't change while connected through the account, as a route
// Save can't, and starting keeps it.
func TestLANScanLockedWhileConnected(t *testing.T) {
	s, _ := copyServer(t)
	if w := doPost(s, "/api/lan-scan", `{"settings":{"lan_scan":"off"}}`); w.Code != 200 {
		t.Fatalf("lan-scan = %d %s", w.Code, w.Body)
	}
	release := make(chan struct{})
	s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status, _ func(proxy.Dialer) (func(), bool)) error {
		select {
		case <-ctx.Done():
		case <-release:
		}
		return nil
	}
	in, _ := json.Marshal(input{Settings: testSettings(), Password: testPassword})
	if w := doPost(s, "/api/start", string(in)); w.Code != 200 {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	if got, _ := loadSettings(s.storage); got.LANScan != "off" {
		t.Fatalf("starting dropped it: %+v", got)
	}
	w := doPost(s, "/api/lan-scan", `{"settings":{"lan_scan":"connect"}}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"route_locked"`) {
		t.Fatalf("lan-scan while connected = %d %s", w.Code, w.Body)
	}
	close(release)
	s.controller.quit()
	if got, _ := loadSettings(s.storage); got.LANScan != "off" {
		t.Fatalf("a locked change saved: %+v", got)
	}
}

// moveSerials makes 192.0.2.30, where SERIAL-A answered, answer as
// SERIAL-B, and SERIAL-A answer at 192.0.2.40; both connect.
func moveSerials(l *lanTCP, n *lanNet) {
	n.set(map[netip.Addr][3]string{
		netip.MustParseAddr("192.0.2.30"): {"SERIAL-B", "Other", "Seestar S30"},
		netip.MustParseAddr("192.0.2.40"): {"SERIAL-A", "S50_A", "Seestar S50"},
	})
	l.setUp("192.0.2.30:445", "192.0.2.40:445")
}

// A change of mode is an explicit save: a start-mode run doesn't reuse
// an address its scan found before one, though that address still
// connects, now to another telescope.
func TestModeChangeDoesNotReviveRejectedAddress(t *testing.T) {
	s, l, n := scanModeServer(t, "start")
	o := copyMountOptions(s, copyapp.MountOptions{})
	ctx := landiscovery.WithRun(t.Context(), &landiscovery.Run{})
	connect := func() {
		t.Helper()
		target, err := s.copyTarget()
		if err != nil {
			t.Fatal(err)
		}
		session, err := o.Dial(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		session.Close()
	}
	connect()
	l.take()
	moveSerials(l, n)
	if w := doPost(s, "/api/lan-scan", `{"settings":{"lan_scan":"connect"}}`); w.Code != 200 {
		t.Fatalf("setting: %d %s", w.Code, w.Body)
	}
	connect()
	if calls := l.take(); len(calls) != 1 || !strings.HasSuffix(calls[0], "192.0.2.40:445") {
		t.Fatalf("connect mode didn't choose the matching telescope: %q", calls)
	}
	if w := doPost(s, "/api/lan-scan", `{"settings":{"lan_scan":"start"}}`); w.Code != 200 {
		t.Fatalf("setting: %d %s", w.Code, w.Body)
	}
	scans := n.scans()
	connect()
	if calls := l.take(); len(calls) != 1 || !strings.HasSuffix(calls[0], "192.0.2.40:445") {
		t.Fatalf("returning to start revived a rejected address: %q", calls)
	}
	if n.scans() != scans+1 {
		t.Fatalf("returning to start scanned %d times", n.scans()-scans)
	}
	// The run keeps what the new mode's scan found.
	connect()
	if calls := l.take(); len(calls) != 1 || !strings.HasSuffix(calls[0], "192.0.2.40:445") || n.scans() != scans+1 {
		t.Fatalf("after the new scan: %q, %d scans", calls, n.scans()-scans)
	}
}

// A connect whose search began before a change of mode doesn't make its
// finding the run's: a connect after the change scans for itself, whether
// it waited for that search or came after it.
func TestModeChangeDuringASearch(t *testing.T) {
	s, l, n := scanModeServer(t, "start")
	o := copyMountOptions(s, copyapp.MountOptions{})
	ctx := landiscovery.WithRun(t.Context(), &landiscovery.Run{})
	connect := func() error {
		target, err := s.copyTarget()
		if err != nil {
			return err
		}
		session, err := o.Dial(ctx, target)
		if err == nil {
			session.Close()
		}
		return err
	}
	l.gate, l.entered = make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() { first <- connect() }()
	<-l.entered // the first connect's scan found 192.0.2.30, and its dial waits
	moveSerials(l, n)
	for _, mode := range []string{"connect", "start"} {
		if w := doPost(s, "/api/lan-scan", `{"settings":{"lan_scan":"`+mode+`"}}`); w.Code != 200 {
			t.Fatalf("setting %s: %d %s", mode, w.Code, w.Body)
		}
	}
	second := make(chan error, 1)
	go func() { second <- connect() }()
	close(l.gate)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	calls := l.take()
	if len(calls) != 2 || !strings.HasSuffix(calls[0], "192.0.2.30:445") || !strings.HasSuffix(calls[1], "192.0.2.40:445") {
		t.Fatalf("the connect after the change reused the older finding: %q", calls)
	}
	if n.scans() != 2 {
		t.Fatalf("scans %d, want 2", n.scans())
	}
	// The older search saved nothing over the explicit save.
	if got, _ := loadSettings(s.storage); got.LANAddress != "192.0.2.40:445" {
		t.Fatalf("saved %q", got.LANAddress)
	}
	if err := connect(); err != nil {
		t.Fatal(err)
	}
	if calls := l.take(); len(calls) != 1 || !strings.HasSuffix(calls[0], "192.0.2.40:445") || n.scans() != 2 {
		t.Fatalf("the run didn't keep the newer finding: %q, %d scans", calls, n.scans())
	}
}

// A changed serial searches in the same run, though the earlier
// telescope's address still connects.
func TestRunSerialChangeSearches(t *testing.T) {
	s, l, n := scanModeServer(t, "start")
	ctx := landiscovery.WithRun(t.Context(), &landiscovery.Run{})
	if got := modeDials(t, s, n, ctx, 1); got != 1 {
		t.Fatalf("first scans %d", got)
	}
	l.take()
	if err := s.controller.persistRoute(routeLAN, "192.0.2.20:445", "SERIAL-B"); err != nil {
		t.Fatal(err)
	}
	n.set(map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.20"): {"SERIAL-B", "Other", "Seestar S30"}})
	l.setUp("192.0.2.20:445", "192.0.2.30:445")
	if got := modeDials(t, s, n, ctx, 1); got != 1 {
		t.Fatalf("changed serial scans %d", got)
	}
	if calls := l.take(); len(calls) != 1 || !strings.HasSuffix(calls[0], "192.0.2.20:445") {
		t.Fatalf("serial change reused the earlier telescope: %q", calls)
	}
}
