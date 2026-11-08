package ui

// These are the account route's adapter tests: the Mount is built by
// copyMountOptions, with remote.DialVia's go-smb2 client replaced by a
// stand-in over the same supplied channel and a fake proxy.Dialer in
// place of the session. They prove the host's routing and the run's
// lifetime rules, not the session's demand behavior.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
)

// heldPing is the byte a stand-in sends for a request the echo peer
// never answers; any other byte is echoed.
const heldPing = 'h'

// orderLog records events in the order they happen.
type orderLog struct {
	mu     sync.Mutex
	events []string
}

func (l *orderLog) add(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *orderLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// echoDialer is a session's dialer whose every channel reaches an echo
// peer. held is told of each held request the peer receives, and log
// of each channel the client side closes.
type echoDialer struct {
	held  chan struct{}
	log   *orderLog
	mu    sync.Mutex
	ports []uint16
}

func (d *echoDialer) DialPort(_ context.Context, port uint16) (net.Conn, error) {
	d.mu.Lock()
	d.ports = append(d.ports, port)
	d.mu.Unlock()
	client, peer := net.Pipe()
	go func() {
		defer peer.Close()
		b := make([]byte, 1)
		for {
			if _, err := peer.Read(b); err != nil {
				return
			}
			if b[0] == heldPing {
				if d.held != nil {
					d.held <- struct{}{}
				}
				continue
			}
			if _, err := peer.Write(b); err != nil {
				return
			}
		}
	}()
	return &loggedConn{Conn: client, log: d.log}, nil
}

// dials is how many channels d has opened.
func (d *echoDialer) dials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.ports)
}

// loggedConn records its first Close.
type loggedConn struct {
	net.Conn
	log  *orderLog
	once sync.Once
}

func (c *loggedConn) Close() error {
	c.once.Do(func() {
		if c.log != nil {
			c.log.add("channel closed")
		}
	})
	return c.Conn.Close()
}

// channelSession is the stand-in's session: a share in a local folder
// whose every call first round-trips one byte over the supplied channel,
// so a closed or revoked channel fails the next file operation, as it
// fails go-smb2's. Requests on one channel are one at a time.
type channelSession struct {
	*remote.FakeDir
	name string
	c    net.Conn
	held func(string) bool
	wrap func(*channelSession, string, remote.File) remote.File // if set, wraps each file opened
	mu   sync.Mutex
}

func (s *channelSession) ping(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := []byte{'p'}
	if s.held != nil && s.held(path) {
		b[0] = heldPing
	}
	if _, err := s.c.Write(b); err != nil {
		return fmt.Errorf("%s: %w", s.name, err)
	}
	if _, err := io.ReadFull(s.c, b); err != nil {
		return fmt.Errorf("%s: %w", s.name, err)
	}
	return nil
}

func (s *channelSession) Walk(ctx context.Context, skip func(string, bool) bool, kept func(remote.Entry)) ([]remote.Entry, error) {
	if err := s.ping("."); err != nil {
		return nil, err
	}
	return s.FakeDir.Walk(ctx, skip, kept)
}

func (s *channelSession) ReadDir(ctx context.Context, dir string) ([]remote.Entry, error) {
	if err := s.ping(dir); err != nil {
		return nil, err
	}
	return s.FakeDir.ReadDir(ctx, dir)
}

func (s *channelSession) Open(ctx context.Context, p string) (remote.File, error) {
	if err := s.ping(p); err != nil {
		return nil, err
	}
	f, err := s.FakeDir.Open(ctx, p)
	if err != nil || s.wrap == nil {
		return f, err
	}
	return s.wrap(s, p, f), nil
}

func (s *channelSession) RemoveFile(ctx context.Context, e remote.Entry) error {
	if err := s.ping(e.Path); err != nil {
		return err
	}
	return s.FakeDir.RemoveFile(ctx, e)
}

func (s *channelSession) RemoveDir(ctx context.Context, p string) error {
	if err := s.ping(p); err != nil {
		return err
	}
	return s.FakeDir.RemoveDir(ctx, p)
}

func (s *channelSession) Close() error {
	err := s.c.Close()
	return errors.Join(s.FakeDir.Close(), err)
}

// viaEnv is a routeEnv whose Mount is the app's, from copyMountOptions,
// over a telescope share in a local folder. Through the account the
// stand-in reaches it over the run's channel; on this network a TCP
// stand-in reaches it directly.
type viaEnv struct {
	*routeEnv
	shareDir string
	held     func(string) bool // requests the echo peer holds
	wrap     func(*channelSession, string, remote.File) remote.File
	stateDir string // the Mount's history root

	mu       sync.Mutex
	names    []string // the name each account dial gave
	tcp      []string // the endpoints dialed over TCP
	removers int      // removers opened through the account
}

func newViaEnv(t *testing.T, settings *copyapp.Settings) *viaEnv {
	t.Helper()
	e := &viaEnv{routeEnv: newRouteEnv(t), shareDir: filepath.Join(t.TempDir(), "share")}
	e.s.copy.Quit()
	e.file("MyWorks/a.jpg", "AAA")
	saved := [...]any{dialVia, removerVia, tcpDial, tcpRemover}
	t.Cleanup(func() {
		dialVia = saved[0].(func(context.Context, string, remote.Conn, *slog.Logger) (remote.Session, error))
		removerVia = saved[1].(func(context.Context, string, remote.Conn, *slog.Logger) (remote.Remover, error))
		tcpDial = saved[2].(func(context.Context, string, *slog.Logger) (remote.Session, error))
		tcpRemover = saved[3].(func(context.Context, string, *slog.Logger) (remote.Remover, error))
	})
	open := func(ctx context.Context, name string, conn remote.Conn) (*channelSession, error) {
		c, err := conn(ctx)
		if err != nil {
			return nil, fmt.Errorf("connect to %s: %w", name, err)
		}
		d, err := remote.NewFakeDir(e.shareDir)
		if err != nil {
			c.Close()
			return nil, err
		}
		return &channelSession{FakeDir: d, name: name, c: c, held: e.held, wrap: e.wrap}, nil
	}
	dialVia = func(ctx context.Context, name string, conn remote.Conn, _ *slog.Logger) (remote.Session, error) {
		e.mu.Lock()
		e.names = append(e.names, name)
		e.mu.Unlock()
		s, err := open(ctx, name, conn)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	removerVia = func(ctx context.Context, name string, conn remote.Conn, _ *slog.Logger) (remote.Remover, error) {
		e.mu.Lock()
		e.removers++
		e.mu.Unlock()
		s, err := open(ctx, name, conn)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	tcpDial = func(_ context.Context, endpoint string, _ *slog.Logger) (remote.Session, error) {
		e.mu.Lock()
		e.tcp = append(e.tcp, endpoint)
		e.mu.Unlock()
		return remote.NewFakeDir(e.shareDir)
	}
	tcpRemover = func(_ context.Context, endpoint string, _ *slog.Logger) (remote.Remover, error) {
		e.mu.Lock()
		e.tcp = append(e.tcp, endpoint)
		e.mu.Unlock()
		return remote.NewFakeDir(e.shareDir)
	}
	e.mount(settings)
	return e
}

// mount replaces e's Mount with the app's, from copyMountOptions, with
// saved copy settings if not nil.
func (e *viaEnv) mount(saved *copyapp.Settings) {
	test := func(context.Context, string) ([]remote.Entry, error) {
		return []remote.Entry{{Path: "MyWorks", Kind: remote.Dir}}, nil
	}
	e.stateDir = filepath.Join(e.t.TempDir(), "state")
	e.s.copy = copyapp.NewMount(context.Background(), copyMountOptions(e.s, copyapp.MountOptions{
		Addr: netip.MustParseAddrPort(e.s.host), StateDir: e.stateDir, Version: "synthetic",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Settings: saved, Test: test, Reach: reachBy(test),
	}))
	e.t.Cleanup(e.s.copy.Quit)
}

// file writes a file of the telescope's share, older than any keep
// window.
func (e *viaEnv) file(name, data string) {
	e.t.Helper()
	p := filepath.Join(e.shareDir, filepath.FromSlash(name))
	mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		e.t.Fatal(err)
	}
}

func (e *viaEnv) accountDials() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.names...)
}

func (e *viaEnv) tcpDials() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.tcp...)
}

func (e *viaEnv) removersOpened() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.removers
}

// copiedFile reads the copy of name in the copy folder.
func (e *viaEnv) copiedFile(name string) string {
	b, _ := os.ReadFile(filepath.Join(e.folder, filepath.FromSlash(name)))
	return string(b)
}

// mirrored is a fake run in runProxy's order: it selects its telescope,
// waits at gate, attaches its dialer, arms, and runs until its context
// ends or end closes; then it detaches, logs that, and waits at hold
// before it returns.
type mirrored struct {
	serial   string
	d        proxy.Dialer
	gate     chan struct{} // if not nil, held before the attach
	end      chan struct{} // if not nil, ends the run as a session failure would
	hold     chan struct{} // if not nil, held after the detach
	log      *orderLog
	attached chan struct{} // closed once attached and armed
	detached chan struct{} // closed once detached
	refused  atomic.Bool
}

func newMirrored(serial string, d proxy.Dialer) *mirrored {
	return &mirrored{serial: serial, d: d, attached: make(chan struct{}), detached: make(chan struct{})}
}

func (m *mirrored) run(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status, attach func(proxy.Dialer) (func(), bool)) error {
	status.SetSelectedDevice(cloudhttp.Device{Serial: m.serial, Model: "Seestar S50"})
	if m.gate != nil {
		<-m.gate
	}
	detach, ok := attach(m.d)
	if !ok {
		m.refused.Store(true)
		close(m.detached)
		return ctx.Err()
	}
	status.MarkArmed()
	close(m.attached)
	select {
	case <-ctx.Done():
	case <-m.end:
	}
	detach()
	if m.log != nil {
		m.log.add("detached")
	}
	close(m.detached)
	if m.hold != nil {
		<-m.hold
	}
	return nil
}

// connectWith starts a run of m through the account and waits for it to
// attach, unless it is gated.
func (e *viaEnv) connectWith(m *mirrored, settings Settings) {
	e.t.Helper()
	e.s.controller.run = m.run
	if code, body := e.start(settings); code != 200 {
		e.t.Fatalf("Connect = %d %s", code, body)
	}
	if m.gate == nil {
		e.awaitChan(m.attached, "the run to attach")
	}
}

func (e *viaEnv) awaitChan(c <-chan struct{}, what string) {
	e.t.Helper()
	select {
	case <-c:
	case <-time.After(10 * time.Second):
		e.t.Fatalf("timed out waiting for %s", what)
	}
}

// stopRun stops the current run and waits for it to end.
func (e *viaEnv) stopRun() {
	e.s.controller.stop()
	e.until("the run to end", func() bool { _, active := e.s.controller.snapshot(); return !active })
}

// browseList lists dir in Browse.
func (e *viaEnv) browseList(dir string) (int, string) {
	return e.get("/browse/api/list?path=" + dir)
}

// An address on this network is dialed over TCP, whatever its spelling,
// even while a run through the account is attached: the route is the
// target's run, not its text.
func TestAdapterLANHostnameUsesTCP(t *testing.T) {
	e := newViaEnv(t, nil)
	d := &echoDialer{}
	e.connectWith(newMirrored("synthetic-device-a", d), testSettings())
	// Connect chooses the account route and a save waits for the run's
	// end, so the test sets the route the run's file tools see itself.
	e.s.mu.Lock()
	e.s.settings.Route, e.s.settings.LANAddress = routeLAN, "tunnel-7:445"
	e.s.mu.Unlock()
	if code, body := e.browseList("MyWorks"); code != 200 || !strings.Contains(body, "a.jpg") {
		t.Fatalf("Browse = %d %s", code, body)
	}
	e.settings()
	e.copied()
	if got := e.copiedFile("MyWorks/a.jpg"); got != "AAA" {
		t.Errorf("copied %q", got)
	}
	tcp := e.tcpDials()
	if len(tcp) == 0 {
		t.Error("nothing was dialed over TCP")
	}
	for _, endpoint := range tcp {
		if endpoint != "tunnel-7:445" {
			t.Errorf("TCP dialed %q", endpoint)
		}
	}
	if n := d.dials(); n != 0 || len(e.accountDials()) != 0 {
		t.Errorf("the session was dialed %d times, the account route %d", n, len(e.accountDials()))
	}
}

// Through the account, Copy, Browse and Free up space work with the SMB
// port off: they dial device port 445 through the run's session, and
// the status names nothing missing.
func TestAdapterFileToolsWorkWithSMBOff(t *testing.T) {
	e := newViaEnv(t, nil)
	d := &echoDialer{}
	settings := testSettings()
	settings.Ports = []proxyrun.Port{{Local: 8080, Device: 80}, {Local: 0, Device: 445}}
	e.connectWith(newMirrored("synthetic-device-a", d), settings)
	if got := e.copyStatus(); !got.Unlocked || got.Reach != "" {
		t.Errorf("status with SMB off = %+v", got)
	}
	e.settings()
	e.copied()
	if got := e.copiedFile("MyWorks/a.jpg"); got != "AAA" {
		t.Errorf("copied %q", got)
	}
	if code, body := e.browseList("MyWorks"); code != 200 || !strings.Contains(body, "a.jpg") {
		t.Errorf("Browse = %d %s", code, body)
	}
	if code, body := e.post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`); code != 200 {
		t.Fatalf("find = %d %s", code, body)
	}
	e.until("the preview", func() bool { state, _ := e.free(); return state == "preview" })
	for _, name := range e.accountDials() {
		if name != accountEndpoint {
			t.Errorf("an account dial named %q", name)
		}
	}
	d.mu.Lock()
	ports := append([]uint16(nil), d.ports...)
	d.mu.Unlock()
	if len(ports) < 3 {
		t.Errorf("the session had %d channels, want one each for Copy, Browse and the preview", len(ports))
	}
	for _, p := range ports {
		if p != 445 {
			t.Errorf("a channel to port %d", p)
		}
	}
	if tcp := e.tcpDials(); len(tcp) != 0 {
		t.Errorf("TCP dialed %q", tcp)
	}
}

// A run's end closes every channel its file tools hold, a Browse listing
// and a pass both mid-request, and joins those closes before detach
// returns and the session closes; both requests fail.
func TestAdapterDetachClosesAndJoins(t *testing.T) {
	e := newViaEnv(t, nil)
	e.file("MyWorks/held.fit", "held")
	e.file("Held/b.jpg", "BBB")
	e.held = func(p string) bool { return p == "MyWorks/held.fit" || p == "Held" }
	log := &orderLog{}
	d := &echoDialer{held: make(chan struct{}, 2), log: log}
	m := newMirrored("synthetic-device-a", d)
	m.log, m.end = log, make(chan struct{})
	e.connectWith(m, testSettings())
	e.settings()
	if code, body := e.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
		t.Fatalf("start copying = %d %s", code, body)
	}
	listed := make(chan int, 1)
	go func() { code, _ := e.browseList("Held"); listed <- code }()
	e.awaitChan(d.held, "the first held request")
	e.awaitChan(d.held, "the second held request")
	close(m.end)
	e.awaitChan(m.detached, "the detach")
	log.add("session closed")
	if got, want := log.list(), []string{"channel closed", "channel closed", "detached", "session closed"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order %q, want %q", got, want)
	}
	select {
	case code := <-listed:
		if code == 200 {
			t.Error("the held Browse listing succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the held Browse listing didn't return")
	}
	e.until("the pass to fail", func() bool { state, _ := e.copying(); return state != "copying" && state != "" })
	if got := e.copiedFile("MyWorks/held.fit"); got != "" {
		t.Errorf("the held file copied: %q", got)
	}
}

// targetOf is the copy target now, which must be the account's.
func (e *viaEnv) targetOf(what, serial string) copyapp.Target {
	e.t.Helper()
	tg, err := e.s.copyTarget()
	if err != nil || tg.Endpoint != accountEndpoint || tg.Run == 0 || !strings.Contains(tg.Device, serial) {
		e.t.Fatalf("target %s = %q, %q, run %d, %v", what, tg.Endpoint, tg.Device, tg.Run, err)
	}
	return tg
}

// Copying through the account follows a reconnect to the same telescope:
// each run's target names it, and the next pass dials the new run.
func TestAdapterSameDeviceResume(t *testing.T) {
	e := newViaEnv(t, nil)
	d1, d2 := &echoDialer{}, &echoDialer{}
	e.connectWith(newMirrored("synthetic-device-a", d1), testSettings())
	first := e.targetOf("in run 1", "synthetic-device-a")
	e.settings()
	if code, body := e.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
		t.Fatalf("start copying = %d %s", code, body)
	}
	e.until("run 1's pass", func() bool { return e.copiedFile("MyWorks/a.jpg") == "AAA" })
	e.until("run 1's pass to end", func() bool { state, _ := e.copying(); return state == "uptodate" })
	e.stopRun()
	e.file("MyWorks/c.jpg", "CCC")
	e.connectWith(newMirrored("synthetic-device-a", d2), testSettings())
	second := e.targetOf("in run 2", "synthetic-device-a")
	if second.Run == first.Run || second.Device != first.Device {
		t.Errorf("run 2's target is run %d at %q, run 1's run %d at %q", second.Run, second.Device, first.Run, first.Device)
	}
	e.until("run 2's pass", func() bool {
		e.post("/copy/api/copy", `{"action":"check"}`)
		return e.copiedFile("MyWorks/c.jpg") == "CCC"
	})
	if d2.dials() == 0 {
		t.Error("run 2's session was never dialed")
	}
	if !e.s.copy.Busy() {
		t.Error("copying stopped after a reconnect to the same telescope")
	}
}

// A reconnect to another telescope ends copying before its pass dials
// the new run.
func TestAdapterOtherDeviceRefused(t *testing.T) {
	e := newViaEnv(t, nil)
	d1, d2 := &echoDialer{}, &echoDialer{}
	e.connectWith(newMirrored("synthetic-device-a", d1), testSettings())
	e.settings()
	if code, body := e.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
		t.Fatalf("start copying = %d %s", code, body)
	}
	e.until("run 1's pass", func() bool { state, _ := e.copying(); return state == "uptodate" })
	e.stopRun()
	e.connectWith(newMirrored("synthetic-device-b", d2), testSettings())
	e.targetOf("in run 2", "synthetic-device-b")
	e.until("copying to end", func() bool {
		e.post("/copy/api/copy", `{"action":"check"}`)
		return !e.s.copy.Busy()
	})
	if state, title := e.copying(); state != "cantwrite" || !strings.Contains(title, "another telescope") {
		t.Errorf("copying ended in %q: %q", state, title)
	}
	if n := d2.dials(); n != 0 {
		t.Errorf("the other telescope's session was dialed %d times", n)
	}
}

// A Free up space preview found in one run doesn't confirm a delete
// after a reconnect, even to the same telescope: it is refused before
// any remover is opened. Connect's route change already clears the
// preview, so the second half starts the next run without it, as only
// the comparison of the preview's run then refuses the delete.
func TestAdapterDelayedDeleteAcrossRestart(t *testing.T) {
	e := newViaEnv(t, nil)
	d1, d2 := &echoDialer{}, &echoDialer{}
	e.connectWith(newMirrored("synthetic-device-a", d1), testSettings())
	e.settings()
	e.copied()
	if code, body := e.post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`); code != 200 {
		t.Fatalf("find = %d %s", code, body)
	}
	e.until("the preview", func() bool { state, _ := e.free(); return state == "preview" })
	_, id := e.free()
	e.stopRun()
	e.connectWith(newMirrored("synthetic-device-a", d2), testSettings())
	if code, body := e.post("/copy/api/delete", `{"action":"delete","id":`+strconv.Itoa(id)+`,"folders":["MyWorks"]}`); code != 409 || !strings.Contains(body, "Preview again") {
		t.Errorf("delete with the last run's preview = %d %s", code, body)
	}
	if n := e.removersOpened(); n != 0 {
		t.Errorf("%d removers opened", n)
	}
	if n := d2.dials(); n != 0 {
		t.Errorf("run 2's session was dialed %d times", n)
	}
	if _, err := os.Stat(filepath.Join(e.shareDir, "MyWorks", "a.jpg")); err != nil {
		t.Errorf("the telescope's file: %v", err)
	}

	if code, body := e.post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`); code != 200 {
		t.Fatalf("find in run 2 = %d %s", code, body)
	}
	e.until("run 2's preview", func() bool { state, _ := e.free(); return state == "preview" })
	_, id = e.free()
	e.stopRun()
	d3 := &echoDialer{}
	m := newMirrored("synthetic-device-a", d3)
	e.s.controller.run = m.run
	if err := e.s.controller.start(testSettings(), testPassword); err != nil {
		t.Fatalf("start run 3: %v", err)
	}
	e.awaitChan(m.attached, "run 3 to attach")
	if state, _ := e.free(); state != "preview" {
		t.Fatalf("run 2's preview is %q in run 3", state)
	}
	if code, body := e.post("/copy/api/delete", `{"action":"delete","id":`+strconv.Itoa(id)+`,"folders":["MyWorks"]}`); code != 409 || !strings.Contains(body, "Preview again") {
		t.Errorf("delete in run 3 with run 2's preview = %d %s", code, body)
	}
	if n := e.removersOpened(); n != 0 || d3.dials() != 0 {
		t.Errorf("in run 3: %d removers opened, %d channels", n, d3.dials())
	}
	if _, err := os.Stat(filepath.Join(e.shareDir, "MyWorks", "a.jpg")); err != nil {
		t.Errorf("the telescope's file after run 3's delete: %v", err)
	}
}

// The file tools have no account target while a run is open but not yet
// attached, nor once it has detached on its way out: Browse and the
// target refuse and nothing is dialed. Between the two, Browse dials the
// run's session.
func TestAdapterNoTargetBeforeAttach(t *testing.T) {
	e := newViaEnv(t, nil)
	d := &echoDialer{}
	m := newMirrored("synthetic-device-a", d)
	m.gate, m.end, m.hold = make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(m.hold)
	e.connectWith(m, testSettings())
	refused := func(what string) {
		t.Helper()
		if _, err := e.s.copyTarget(); !errors.Is(err, errNotConnected) {
			t.Errorf("target %s: %v", what, err)
		}
		if code, body := e.browseList("MyWorks"); code == 200 {
			t.Errorf("Browse %s = %d %s", what, code, body)
		}
	}
	refused("before the attach")
	if n := d.dials(); n != 0 || len(e.accountDials()) != 0 {
		t.Errorf("before the attach: %d channels, %d dials", n, len(e.accountDials()))
	}
	close(m.gate)
	e.awaitChan(m.attached, "the attach")
	if code, body := e.browseList("MyWorks"); code != 200 || !strings.Contains(body, "a.jpg") {
		t.Errorf("Browse once attached = %d %s", code, body)
	}
	if d.dials() != 1 {
		t.Errorf("once attached: %d channels, want 1", d.dials())
	}
	close(m.end)
	e.awaitChan(m.detached, "the detach")
	refused("after the detach")
	if d.dials() != 1 {
		t.Errorf("after the detach: %d channels, want 1", d.dials())
	}
}

// Copying that starts as the app opens waits for the run to attach, and
// then copies through it.
func TestAdapterCopyOnceWaitsForAttach(t *testing.T) {
	e := newViaEnv(t, nil)
	e.s.copy.Quit()
	saved := copyapp.DefaultSettings()
	saved.Folder, saved.StartCopying = e.folder, true
	e.mount(&saved)
	d := &echoDialer{}
	m := newMirrored("synthetic-device-a", d)
	m.gate = make(chan struct{})
	e.connectWith(m, testSettings())
	e.s.copy.Start()
	time.Sleep(2500 * time.Millisecond) // more than two of copy-once's looks
	if got := e.copiedFile("MyWorks/a.jpg"); got != "" || d.dials() != 0 || len(e.accountDials()) != 0 {
		t.Errorf("before the attach: copied %q, %d channels, %d dials", got, d.dials(), len(e.accountDials()))
	}
	close(m.gate)
	e.until("copy-once's copy", func() bool { return e.copiedFile("MyWorks/a.jpg") == "AAA" })
	if d.dials() == 0 {
		t.Error("copy-once didn't copy through the run")
	}
}

// A target captured in one run is refused after Stop and in the next
// run, where the run's own target dials.
func TestAdapterCapturedTargetRefused(t *testing.T) {
	e := newViaEnv(t, nil)
	opts := copyMountOptions(e.s, copyapp.MountOptions{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	d1, d2 := &echoDialer{}, &echoDialer{}
	e.connectWith(newMirrored("synthetic-device-a", d1), testSettings())
	captured := e.targetOf("in run 1", "synthetic-device-a")
	e.stopRun()
	if s, err := opts.Dial(t.Context(), captured); !errors.Is(err, errNotConnected) {
		if s != nil {
			s.Close()
		}
		t.Errorf("dial run 1's target after Stop = %v", err)
	}
	e.connectWith(newMirrored("synthetic-device-a", d2), testSettings())
	if s, err := opts.Dial(t.Context(), captured); !errors.Is(err, errNotConnected) {
		if s != nil {
			s.Close()
		}
		t.Errorf("dial run 1's target in run 2 = %v", err)
	}
	if r, err := opts.Remover(t.Context(), captured); !errors.Is(err, errNotConnected) {
		if r != nil {
			r.Close()
		}
		t.Errorf("open a remover at run 1's target in run 2 = %v", err)
	}
	s, err := opts.Dial(t.Context(), e.targetOf("in run 2", "synthetic-device-a"))
	if err != nil {
		t.Fatalf("dial run 2's target: %v", err)
	}
	if _, err := s.ReadDir(t.Context(), "MyWorks"); err != nil {
		t.Errorf("ReadDir through run 2: %v", err)
	}
	s.Close()
	if d1.dials() != 0 || d2.dials() != 1 {
		t.Errorf("run 1's session dialed %d times, run 2's %d; want 0, 1", d1.dials(), d2.dials())
	}
}

// The live target is the run, whether or not the SMB port is on.
func TestLiveTargetIsTyped(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ports []proxyrun.Port
	}{
		{"smb on", []proxyrun.Port{{Local: 4445, Device: 445}}},
		{"smb off", []proxyrun.Port{{Local: 8080, Device: 80}, {Local: 0, Device: 445}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newViaEnv(t, nil)
			settings := testSettings()
			settings.Ports = tc.ports
			e.s.settings = settings
			e.connectWith(newMirrored("synthetic-device-a", &echoDialer{}), settings)
			e.targetOf("connected", "synthetic-device-a")
		})
	}
}
