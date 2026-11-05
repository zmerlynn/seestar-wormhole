package ui

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
)

// routeEnv is a server with copy mounted over a share at each endpoint:
// a folder holding MyWorks/at_<host>.jpg. Its fakes record each endpoint
// reached and each session dialed, and can hold a dial, a delete's
// remover or a Test.
type routeEnv struct {
	t      *testing.T
	s      *server
	shares string
	folder string

	mu         sync.Mutex
	dialed     []string
	sessions   []*routeSession
	holdDial   chan struct{} // if not nil, a dial waits for it
	holdRemove chan struct{} // if not nil, a remover waits for it
	removing   chan struct{} // if not nil, a remover signals it on entry
	holdTest   chan struct{} // if not nil, a Test waits for it
	tested     chan struct{} // if not nil, a Test signals it on entry
}

func newRouteEnv(t *testing.T) *routeEnv {
	t.Helper()
	base := t.TempDir()
	e := &routeEnv{t: t, s: testServer(t), shares: filepath.Join(base, "shares"), folder: filepath.Join(base, "Seestar")}
	e.s.copy = copyapp.NewMount(context.Background(), copyapp.MountOptions{
		Addr: netip.MustParseAddrPort(e.s.host), StateDir: filepath.Join(base, "state"), Version: "synthetic",
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Target: e.s.copyTarget,
		Dial: func(ctx context.Context, endpoint string) (remote.Session, error) {
			if err := e.wait(ctx, &e.holdDial, nil); err != nil {
				return nil, err
			}
			d, err := e.share(endpoint)
			if err != nil {
				return nil, err
			}
			s := &routeSession{Session: d, ctx: ctx}
			e.mu.Lock()
			e.dialed = append(e.dialed, endpoint)
			e.sessions = append(e.sessions, s)
			e.mu.Unlock()
			return s, nil
		},
		Test: func(ctx context.Context, endpoint string) ([]remote.Entry, error) {
			if err := e.wait(ctx, &e.holdTest, &e.tested); err != nil {
				return nil, err
			}
			if strings.HasPrefix(endpoint, "192.0.2.99:") {
				return nil, errors.New("synthetic unreachable")
			}
			return []remote.Entry{{Path: "MyWorks", Kind: remote.Dir}}, nil
		},
		Remover: func(ctx context.Context, endpoint string) (remote.Remover, error) {
			if err := e.wait(ctx, &e.holdRemove, &e.removing); err != nil {
				return nil, err
			}
			return e.share(endpoint)
		},
	})
	t.Cleanup(e.s.copy.Quit)
	return e
}

// routeSession is a dialed session that counts its Closes and keeps the
// context it was dialed with.
type routeSession struct {
	remote.Session
	ctx    context.Context
	closes atomic.Int32
}

func (s *routeSession) Close() error {
	s.closes.Add(1)
	return s.Session.Close()
}

// retired reports whether s was closed once and its context ended.
func (s *routeSession) retired() bool {
	return s.closes.Load() == 1 && s.ctx.Err() != nil
}

// dialedSessions is the sessions dialed so far.
func (e *routeEnv) dialedSessions() []*routeSession {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]*routeSession(nil), e.sessions...)
}

// wait signals entered, if set, and waits for hold, if set.
func (e *routeEnv) wait(ctx context.Context, hold, entered *chan struct{}) error {
	e.mu.Lock()
	h := *hold
	var in chan struct{}
	if entered != nil {
		in = *entered
	}
	e.mu.Unlock()
	if in != nil {
		in <- struct{}{}
	}
	if h == nil {
		return nil
	}
	select {
	case <-h:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *routeEnv) set(field *chan struct{}, c chan struct{}) {
	e.mu.Lock()
	*field = c
	e.mu.Unlock()
}

// shareFile is the path of the file in the share at endpoint.
func (e *routeEnv) shareFile(endpoint string) string {
	host, _, _ := strings.Cut(endpoint, ":")
	return filepath.Join(e.shares, strings.ReplaceAll(endpoint, ":", "_"), "MyWorks", "at_"+host+".jpg")
}

// share is the share at endpoint, made on first use with a file older
// than any days option.
func (e *routeEnv) share(endpoint string) (*remote.FakeDir, error) {
	name := e.shareFile(endpoint)
	if _, err := os.Stat(name); errors.Is(err, fs.ErrNotExist) {
		if err := e.put(endpoint, "synthetic", time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)); err != nil {
			return nil, err
		}
	}
	return remote.NewFakeDir(filepath.Dir(filepath.Dir(name)))
}

// put writes the file of the share at endpoint.
func (e *routeEnv) put(endpoint, data string, mtime time.Time) error {
	name := e.shareFile(endpoint)
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
		return err
	}
	return os.Chtimes(name, mtime, mtime)
}

func (e *routeEnv) post(path, body string) (int, string) {
	w := serve(e.s, request(e.s, "POST", path, body))
	return w.Code, w.Body.String()
}

func (e *routeEnv) get(path string) (int, string) {
	r := request(e.s, "GET", path, "")
	if strings.HasPrefix(path, "/browse/") {
		r.Header.Del("X-Seestar-Token")
	}
	w := serve(e.s, r)
	return w.Code, w.Body.String()
}

// lan saves this network's route at address.
func (e *routeEnv) lan(address string) {
	e.t.Helper()
	if code, body := e.post("/api/route", `{"settings":{"route":"lan","lan_address":"`+address+`"}}`); code != 200 {
		e.t.Fatalf("save %s = %d %s", address, code, body)
	}
}

// settings saves copy's settings for e.folder.
func (e *routeEnv) settings() {
	e.t.Helper()
	folder, _ := json.Marshal(e.folder)
	if code, body := e.post("/copy/api/settings", `{"folder":`+string(folder)+`,"interval":"5","files_at_once":"4","excludes":[],"includes":[],"skip_built_in":true,"start_copying":false}`); code != 200 {
		e.t.Fatalf("copy settings = %d %s", code, body)
	}
}

// until polls cond for up to ten seconds.
func (e *routeEnv) until(what string, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			e.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// locked reports whether /api/status says copying or deleting holds the
// route.
func (e *routeEnv) locked() bool {
	_, body := e.get("/api/status")
	return strings.Contains(body, `"copy_busy":true`)
}

// connect starts a run through the account that lasts until the test
// ends or it is stopped.
func (e *routeEnv) connect() (int, string) {
	e.s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status) error {
		<-ctx.Done()
		return nil
	}
	return e.start(testSettings())
}

// account makes Connect's runs last until stopped, and lets a test make
// the current run read Ready.
type account struct {
	e     *routeEnv
	mu    sync.Mutex
	ready *proxy.Status
}

func (e *routeEnv) account() *account {
	a := &account{e: e}
	// Only the run markReady names reads Ready; the rest read their own
	// text, which is Connecting until a proxy session moves it.
	e.s.controller.text = func(st *proxy.Status) string {
		a.mu.Lock()
		defer a.mu.Unlock()
		if st != nil && st == a.ready {
			return "Ready"
		}
		return st.Text()
	}
	e.s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status) error {
		<-ctx.Done()
		return nil
	}
	return a
}

// markReady selects the telescope with serial in the current run and
// makes the run read Ready, as a proxy session selects before it is.
func (a *account) markReady(serial string) {
	c := a.e.s.controller
	c.mu.Lock()
	st := c.status
	c.mu.Unlock()
	st.SetSelectedDevice(cloudhttp.Device{Serial: serial, Model: "Seestar S50"})
	a.mu.Lock()
	a.ready = st
	a.mu.Unlock()
}

// stop stops the run and waits for it to end.
func (a *account) stop() {
	a.e.s.controller.stop()
	a.e.until("the run to end", func() bool { _, active := a.e.s.controller.snapshot(); return !active })
}

// copying is the Copy tab's state and title in copy's status.
func (e *routeEnv) copying() (state, title string) {
	e.t.Helper()
	_, body := e.get("/copy/api/status")
	var v struct {
		Copy struct {
			State string `json:"state"`
			Title string `json:"title"`
		} `json:"copy"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		e.t.Fatalf("copy status %s: %v", body, err)
	}
	return v.Copy.State, v.Copy.Title
}

// dials is the endpoints dialed so far.
func (e *routeEnv) dials() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.dialed...)
}

// start posts Connect with settings.
func (e *routeEnv) start(settings Settings) (int, string) {
	e.t.Cleanup(e.s.controller.quit)
	in, _ := json.Marshal(input{Settings: settings, Password: testPassword})
	return e.post("/api/start", string(in))
}

// A save of the route commits inside copy's admission: a copy start
// that arrives during the commit waits for it, and then runs at the new
// address, never at the old one.
func TestRouteCommitAdmitsOne(t *testing.T) {
	e := newRouteEnv(t)
	e.lan("192.0.2.10")
	e.settings()
	entered, release := make(chan struct{}), make(chan struct{})
	save := e.s.controller.save
	e.s.controller.save = func(set Settings) error {
		close(entered)
		<-release
		return save(set)
	}
	saved := make(chan error, 1)
	go func() { saved <- e.s.saveRoute(t.Context(), routeLAN, "192.0.2.20") }()
	<-entered
	started := make(chan int, 1)
	go func() {
		code, _ := e.post("/copy/api/copy", `{"action":"start"}`)
		started <- code
	}()
	select {
	case code := <-started:
		close(release)
		<-saved
		t.Fatalf("copying started during a route commit: %d", code)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if code := <-started; code != 200 {
		t.Fatalf("start copying = %d", code)
	}
	e.until("a copy dial", func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return len(e.dialed) > 0
	})
	e.mu.Lock()
	dialed := append([]string(nil), e.dialed...)
	e.mu.Unlock()
	for _, endpoint := range dialed {
		if endpoint != "192.0.2.20:445" {
			t.Errorf("copying reached %s after the route moved", endpoint)
		}
	}
}

// Connect, which chooses the account route, is refused while copying on
// this network, and keeps the route. (While copying through the account
// it moves nothing: TestAccountEndpoint.)
func TestConnectWhileCopying(t *testing.T) {
	e := newRouteEnv(t)
	e.lan("192.0.2.10")
	e.settings()
	if code, body := e.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
		t.Fatalf("start copying = %d %s", code, body)
	}
	if code, body := e.connect(); code != 400 || !strings.Contains(body, `"code":"route_locked"`) {
		t.Errorf("Connect while copying on this network = %d %s", code, body)
	}
	if e.s.settings.Route != routeLAN {
		t.Errorf("a refused Connect changed the route: %+v", e.s.settings)
	}
	if _, active := e.s.controller.snapshot(); active {
		t.Error("a refused Connect started a run")
	}
}

// Browse follows each change of route with a new session pool: a save of
// another address, a save of the account route, and Connect. The old
// pool's session is closed and its context ended before the change
// returns.
func TestBrowseFollowsTheRoute(t *testing.T) {
	list := func(e *routeEnv) (int, string) { return e.get("/browse/api/list?path=MyWorks") }
	for _, tc := range []struct {
		name   string
		change func(*routeEnv)
		want   string // "" when Browse cannot reach the telescope
	}{
		{"another address", func(e *routeEnv) { e.lan("192.0.2.20") }, "at_192.0.2.20.jpg"},
		{"account", func(e *routeEnv) {
			if code, body := e.post("/api/route", `{"settings":{"route":"account"}}`); code != 200 {
				e.t.Fatalf("save account = %d %s", code, body)
			}
		}, ""},
		{"Connect", func(e *routeEnv) {
			if code, body := e.connect(); code != 200 {
				e.t.Fatalf("Connect = %d %s", code, body)
			}
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRouteEnv(t)
			e.settings()
			e.lan("192.0.2.10")
			if code, body := list(e); code != 200 || !strings.Contains(body, "at_192.0.2.10.jpg") {
				t.Fatalf("first listing = %d %s", code, body)
			}
			old := e.dialedSessions()
			if len(old) != 1 || old[0].retired() {
				t.Fatalf("%d sessions before the change", len(old))
			}
			tc.change(e)
			if !old[0].retired() {
				t.Errorf("the old session had %d Closes and context error %v when the change returned", old[0].closes.Load(), old[0].ctx.Err())
			}
			code, body := list(e)
			if strings.Contains(body, "at_192.0.2.10.jpg") {
				t.Fatalf("Browse kept the old route's session: %d %s", code, body)
			}
			if tc.want == "" && code == 200 || tc.want != "" && (code != 200 || !strings.Contains(body, tc.want)) {
				t.Errorf("listing after the change = %d %s", code, body)
			}
		})
	}
}

// free is Free up space's state and preview number in copy's status.
func (e *routeEnv) free() (state string, id int) {
	e.t.Helper()
	_, body := e.get("/copy/api/status")
	var v struct {
		Free struct {
			State string `json:"state"`
			ID    int    `json:"id"`
		} `json:"free"`
	}
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		e.t.Fatalf("copy status %s: %v", body, err)
	}
	return v.Free.State, v.Free.ID
}

// copied runs copying until the share's file is in the copy folder.
func (e *routeEnv) copied() {
	e.t.Helper()
	if code, body := e.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
		e.t.Fatalf("start copying = %d %s", code, body)
	}
	e.until("a copied file", func() bool {
		found := false
		filepath.WalkDir(e.folder, func(p string, d fs.DirEntry, err error) error {
			found = found || err == nil && strings.HasSuffix(p, ".jpg")
			return nil
		})
		return found
	})
	if code, body := e.post("/copy/api/copy", `{"action":"stop"}`); code != 200 {
		e.t.Fatalf("stop copying = %d %s", code, body)
	}
	e.until("copying to stop", func() bool { return !e.s.copy.Busy() })
}

// A Save on this network tests the address first and commits only if it
// answers; the route is checked again once the Test is done, and a
// refused save keeps the saved route and file.
func TestLANSaveTestsFirst(t *testing.T) {
	e := newRouteEnv(t)
	e.lan("192.0.2.10")
	path := filepath.Join(e.s.storage.root, "config.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	kept := func(what string) {
		t.Helper()
		if e.s.settings.Route != routeLAN || e.s.settings.LANAddress != "192.0.2.10:445" {
			t.Errorf("%s changed the route: %+v", what, e.s.settings)
		}
		if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
			t.Errorf("%s rewrote the file: %s, %v", what, after, err)
		}
	}
	code, body := e.post("/api/route", `{"settings":{"route":"lan","lan_address":"192.0.2.99"}}`)
	if code != 400 || !strings.Contains(body, `"code":"lan_unreachable"`) || !strings.Contains(body, `"field":"lan_address"`) {
		t.Errorf("save of an unreachable address = %d %s", code, body)
	}
	kept("an unreachable address")

	// Copying that starts during the Test refuses the save after it.
	e.settings()
	tested, hold := make(chan struct{}), make(chan struct{})
	e.set(&e.tested, tested)
	e.set(&e.holdTest, hold)
	saved := make(chan string, 1)
	go func() {
		_, body := e.post("/api/route", `{"settings":{"route":"lan","lan_address":"192.0.2.20"}}`)
		saved <- body
	}()
	select {
	case <-tested:
	case <-time.After(10 * time.Second):
		close(hold)
		t.Fatal("the save didn't test the address")
	}
	e.set(&e.tested, nil)
	if code, body := e.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
		t.Fatalf("start copying = %d %s", code, body)
	}
	close(hold)
	if body := <-saved; !strings.Contains(body, `"code":"route_locked"`) {
		t.Errorf("save once copying started during its Test = %s", body)
	}
	kept("a save refused after its Test")
}

// Free up space's check and delete hold the route as copying does: Busy,
// /api/status and a save of the route say so while either runs, and the
// lock clears once it ends.
func TestFreeUpSpaceLocksTheRoute(t *testing.T) {
	e := newRouteEnv(t)
	e.lan("192.0.2.10")
	e.settings()
	e.copied()
	held := func(what string) {
		t.Helper()
		if !e.s.copy.Busy() || !e.locked() {
			t.Errorf("%s: Busy %v, status locked %v", what, e.s.copy.Busy(), e.locked())
		}
		if code, body := e.post("/api/route", `{"settings":{"route":"lan","lan_address":"192.0.2.20"}}`); code != 400 || !strings.Contains(body, `"code":"route_locked"`) {
			t.Errorf("save during %s = %d %s", what, code, body)
		}
		if code, body := e.post("/api/route", `{"settings":{"route":"account"}}`); code != 400 || !strings.Contains(body, `"code":"route_locked"`) {
			t.Errorf("account save during %s = %d %s", what, code, body)
		}
	}
	cleared := func(what string) {
		t.Helper()
		if e.s.copy.Busy() || e.locked() {
			t.Errorf("after %s: Busy %v, status locked %v", what, e.s.copy.Busy(), e.locked())
		}
	}

	hold := make(chan struct{})
	e.set(&e.holdDial, hold)
	if code, body := e.post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`); code != 200 {
		t.Fatalf("find = %d %s", code, body)
	}
	held("a check")
	close(hold)
	e.set(&e.holdDial, nil)
	e.until("the preview", func() bool { state, _ := e.free(); return state != "checking" })
	state, id := e.free()
	if state != "preview" {
		t.Fatalf("check ended in %q", state)
	}
	cleared("a check")

	hold = make(chan struct{})
	e.set(&e.holdRemove, hold)
	if code, body := e.post("/copy/api/delete", `{"action":"delete","id":`+strconv.Itoa(id)+`,"folders":["MyWorks"]}`); code != 200 {
		t.Fatalf("delete = %d %s", code, body)
	}
	held("a delete")
	close(hold)
	e.until("the delete", func() bool { state, _ := e.free(); return state != "deleting" })
	if state, _ := e.free(); state != "done" {
		t.Errorf("delete ended in %q", state)
	}
	cleared("a delete")
	e.lan("192.0.2.20")
}

// Through the account, the copier reaches the telescope at the run's SMB
// mapping once the run's status reads Ready, and not while it connects,
// stops or reconnects. Connect again while copying through the account
// between passes goes ahead, and copying resumes once the same telescope
// is Ready again, at its new mapping.
func TestAccountEndpoint(t *testing.T) {
	e := newRouteEnv(t)
	a := e.account()
	// A run ends once stopped and released, or once the test has.
	release := make(chan struct{})
	e.s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status) error {
		<-ctx.Done()
		select {
		case <-release:
		case <-t.Context().Done():
		}
		return nil
	}
	settings := testSettings()
	settings.Ports = []proxyrun.Port{{Local: 5445, Device: 445}}
	want := "127.0.0.1:5445"
	endpoint := func(what string, wantErr error) {
		t.Helper()
		got, err := e.s.copyTarget()
		if wantErr != nil && (got.Endpoint != "" || !errors.Is(err, wantErr)) || wantErr == nil && (got.Endpoint != want || err != nil) {
			t.Errorf("endpoint %s = %q, %v", what, got.Endpoint, err)
		}
	}
	listed := func(what string) {
		t.Helper()
		if code, body := e.get("/browse/api/list?path=MyWorks"); code != 200 || !strings.Contains(body, "at_127.0.0.1.jpg") {
			t.Errorf("Browse %s = %d %s", what, code, body)
		}
	}
	passDone := func(what string) {
		t.Helper()
		e.until(what, func() bool { state, _ := e.copying(); return state == "uptodate" })
	}

	if code, body := e.start(settings); code != 200 {
		t.Fatalf("Connect = %d %s", code, body)
	}
	endpoint("while connecting", errNotConnected)
	a.markReady("synthetic-device-a")
	endpoint("once Ready", nil)
	e.settings()
	if code, body := e.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
		t.Fatalf("start copying = %d %s", code, body)
	}
	passDone("the first pass")
	if dialed := e.dials(); len(dialed) != 1 || dialed[0] != want {
		t.Errorf("copying reached %q", dialed)
	}
	listed("once Ready")

	e.s.controller.stop()
	endpoint("while stopping", errNotConnected)
	close(release)
	e.until("the run to end", func() bool { _, active := e.s.controller.snapshot(); return !active })
	endpoint("once stopped", errNotConnected)
	if !e.s.copy.Busy() {
		t.Fatal("copying stopped with the run")
	}

	release = make(chan struct{})
	close(release)
	settings.Ports = []proxyrun.Port{{Local: 6445, Device: 445}}
	want = "127.0.0.1:6445"
	if code, body := e.start(settings); code != 200 {
		t.Fatalf("Connect while copying through the account = %d %s", code, body)
	}
	endpoint("while reconnecting", errNotConnected)
	a.markReady("synthetic-device-a")
	endpoint("once Ready again", nil)
	listed("once Ready again")
	e.until("a pass after the reconnect", func() bool {
		e.post("/copy/api/copy", `{"action":"check"}`)
		return len(e.dials()) > 1
	})
	passDone("the pass after the reconnect")
	if dialed := e.dials(); dialed[len(dialed)-1] != want {
		t.Errorf("after the reconnect copying reached %q", dialed)
	}
	if !e.s.copy.Busy() {
		t.Error("copying stopped after a reconnect to the same telescope")
	}
}

// Copying through the account stays with the telescope it started for:
// once Connect reaches another telescope, with copying on between passes,
// the next pass ends copying before it dials, and copying must be started
// again for the new one. The serial is never shown.
func TestAccountCopyStaysWithItsTelescope(t *testing.T) {
	e := newRouteEnv(t)
	a := e.account()
	atA, atB := testSettings(), testSettings()
	atA.Ports = []proxyrun.Port{{Local: 5445, Device: 445}}
	atB.Ports = []proxyrun.Port{{Local: 6445, Device: 445}}
	// B has a newer file of the same name, which a pass there would copy
	// over A's.
	if err := e.put("127.0.0.1:6445", "BBB", time.Date(2021, 1, 2, 3, 4, 5, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(e.folder, "MyWorks", "at_127.0.0.1.jpg")
	if code, body := e.start(atA); code != 200 {
		t.Fatalf("Connect to A = %d %s", code, body)
	}
	a.markReady("synthetic-device-a")
	e.settings()
	if code, body := e.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
		t.Fatalf("start copying = %d %s", code, body)
	}
	e.until("A's pass", func() bool { state, _ := e.copying(); return state == "uptodate" })
	a.stop()

	if code, body := e.start(atB); code != 200 {
		t.Fatalf("Connect to B with copying on between passes = %d %s", code, body)
	}
	a.markReady("synthetic-device-b")
	e.until("copying to end", func() bool {
		e.post("/copy/api/copy", `{"action":"check"}`)
		return !e.s.copy.Busy()
	})
	state, title := e.copying()
	if state != "cantwrite" || !strings.Contains(title, "another telescope") || strings.Contains(title, "synthetic-device") {
		t.Errorf("copying ended in %q: %q", state, title)
	}
	for _, endpoint := range e.dials() {
		if endpoint != "127.0.0.1:5445" {
			t.Errorf("copying reached %s", endpoint)
		}
	}
	if b, err := os.ReadFile(local); err != nil || string(b) != "synthetic" {
		t.Errorf("A's copy = %q, %v", b, err)
	}

	// Start copying again copies from B.
	if code, body := e.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
		t.Fatalf("start copying again = %d %s", code, body)
	}
	e.until("B's file", func() bool { b, _ := os.ReadFile(local); return string(b) == "BBB" })
}

// A pass that comes due while Connect is admitted, and before it has
// committed, waits for the commit and then follows the run Connect left.
// The old run stops meanwhile and Connect's run reuses its local port: if
// that reaches another telescope, copying ends without dialing it; if it
// reaches the same one, copying resumes there. The first pass fails
// a.jpg, so the next retries it, and each telescope's a.jpg differs.
func TestAccountPassDuringConnect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		serial string // the telescope Connect's run selects
		want   string // a.jpg's copy, or "" for none
	}{
		{"other telescope", "synthetic-device-b", ""},
		{"same telescope", "synthetic-device-a", "AAA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRouteEnv(t)
			e.s.copy.Quit()
			a := e.account()
			var mu sync.Mutex
			dials, armed := 0, false
			entered, release := make(chan struct{}, 1), make(chan struct{})
			// The share is whichever telescope the port reaches when the
			// dial goes through, as a reused local listener would.
			e.s.copy = copyapp.NewMount(context.Background(), copyapp.MountOptions{
				Addr: netip.MustParseAddrPort(e.s.host), StateDir: t.TempDir(), Target: e.s.copyTarget,
				Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
				Dial: func(ctx context.Context, _ string) (remote.Session, error) {
					mu.Lock()
					dials++
					first, held := dials == 1, armed
					armed = false
					mu.Unlock()
					if held {
						// The dial reaches the port once Connect has
						// committed.
						entered <- struct{}{}
						select {
						case <-release:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					_, device, _, ok := e.s.controller.connected()
					if !ok {
						return nil, errNotConnected
					}
					data := "AAA"
					if strings.Contains(device, "synthetic-device-b") {
						data = "BBB"
					}
					f := remote.NewFake(fstest.MapFS{"MyWorks/a.jpg": {Data: []byte(data), ModTime: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)}})
					if first {
						f.Faults = map[string]remote.Fault{"MyWorks/a.jpg": {OpenErr: errors.New("synthetic read failure")}}
					}
					return f, nil
				},
			})
			t.Cleanup(e.s.copy.Quit)
			settings := testSettings()
			if code, body := e.start(settings); code != 200 {
				t.Fatalf("Connect = %d %s", code, body)
			}
			a.markReady("synthetic-device-a")
			e.settings()
			if code, body := e.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
				t.Fatalf("start copying = %d %s", code, body)
			}
			e.until("the first pass", func() bool { state, _ := e.copying(); return state == "failures" })

			mu.Lock()
			armed = true
			mu.Unlock()
			admitted, proceed := make(chan struct{}), make(chan struct{})
			changed := make(chan error, 1)
			go func() {
				changed <- e.s.changeRoute(func() bool { return false }, func() error {
					close(admitted)
					<-proceed
					if err := e.s.controller.start(settings, ""); err != nil {
						return err
					}
					a.markReady(tc.serial)
					return nil
				})
			}()
			<-admitted
			// The next pass comes due and the old run stops. A pass that
			// dials during the change does so well within this window;
			// one that waits for the change never does.
			deadline := time.Now().Add(500 * time.Millisecond)
		due:
			for time.Now().Before(deadline) {
				e.post("/copy/api/copy", `{"action":"check"}`)
				select {
				case <-entered:
					t.Error("the next pass dialed during an admitted Connect")
					break due
				case <-time.After(5 * time.Millisecond):
				}
			}
			a.stop()
			close(proceed)
			if err := <-changed; err != nil {
				t.Fatal(err)
			}
			close(release)
			local := filepath.Join(e.folder, "MyWorks", "a.jpg")
			if tc.want == "" {
				e.until("copying to end", func() bool { _, err := os.Stat(local); return !e.s.copy.Busy() || err == nil })
				if state, title := e.copying(); state != "cantwrite" || !strings.Contains(title, "another telescope") {
					t.Errorf("copying ended in %q: %q", state, title)
				}
				if b, err := os.ReadFile(local); err == nil {
					t.Errorf("a pass copied the other telescope's a.jpg: %q", b)
				}
				return
			}
			e.until("the pass", func() bool {
				e.post("/copy/api/copy", `{"action":"check"}`)
				b, _ := os.ReadFile(local)
				return string(b) == tc.want
			})
		})
	}
}

// A delete through the account holds the connection: Connect is refused
// while it runs, even to the same local port, where another telescope
// could answer, and the route stays locked until it ends.
func TestAccountDeleteHoldsTheConnection(t *testing.T) {
	e := newRouteEnv(t)
	a := e.account()
	settings := testSettings()
	settings.Ports = []proxyrun.Port{{Local: 5445, Device: 445}}
	if code, body := e.start(settings); code != 200 {
		t.Fatalf("Connect = %d %s", code, body)
	}
	a.markReady("synthetic-device-a")
	e.settings()
	e.copied()
	if code, body := e.post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`); code != 200 {
		t.Fatalf("find = %d %s", code, body)
	}
	e.until("the preview", func() bool { state, _ := e.free(); return state == "preview" })
	_, id := e.free()
	hold, removing := make(chan struct{}), make(chan struct{}, 1)
	e.set(&e.holdRemove, hold)
	e.set(&e.removing, removing)
	if code, body := e.post("/copy/api/delete", `{"action":"delete","id":`+strconv.Itoa(id)+`,"folders":["MyWorks"]}`); code != 200 {
		t.Fatalf("delete = %d %s", code, body)
	}
	// The delete's dial is on its way to the port when the run stops.
	<-removing
	a.stop()
	if code, body := e.start(settings); code != 400 || !strings.Contains(body, `"code":"route_locked"`) {
		t.Errorf("Connect on the same port during a delete = %d %s", code, body)
	}
	if _, active := e.s.controller.snapshot(); active {
		t.Error("a refused Connect started a run")
	}
	close(hold)
	e.until("the delete", func() bool { state, _ := e.free(); return state != "deleting" })
	if code, body := e.start(settings); code != 200 {
		t.Errorf("Connect once the delete ended = %d %s", code, body)
	}
}

// A Free up space preview belongs to the run it was found on: after a
// reconnect to the same telescope through the same port, the target is
// another one and the preview no longer confirms a delete.
func TestAccountPreviewEndsWithItsRun(t *testing.T) {
	e := newRouteEnv(t)
	a := e.account()
	settings := testSettings()
	settings.Ports = []proxyrun.Port{{Local: 5445, Device: 445}}
	if code, body := e.start(settings); code != 200 {
		t.Fatalf("Connect = %d %s", code, body)
	}
	a.markReady("synthetic-device")
	e.settings()
	e.copied()
	if code, body := e.post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`); code != 200 {
		t.Fatalf("find = %d %s", code, body)
	}
	e.until("the preview", func() bool { state, _ := e.free(); return state == "preview" })
	_, id := e.free()
	first, err := e.s.copyTarget()
	if err != nil {
		t.Fatal(err)
	}
	a.stop()
	if code, body := e.start(settings); code != 200 {
		t.Fatalf("reconnect = %d %s", code, body)
	}
	a.markReady("synthetic-device")
	if now, err := e.s.copyTarget(); err != nil || now == first || now.Endpoint != first.Endpoint || now.Device != first.Device {
		t.Errorf("the reconnected target is the same run's, or another telescope's: %v", err)
	}
	if code, body := e.post("/copy/api/delete", `{"action":"delete","id":`+strconv.Itoa(id)+`,"folders":["MyWorks"]}`); code != 409 || !strings.Contains(body, "Preview again") {
		t.Errorf("delete with the last run's preview = %d %s", code, body)
	}
	if _, err := os.Stat(e.shareFile("127.0.0.1:5445")); err != nil {
		t.Errorf("the telescope's file: %v", err)
	}
}

// The route and this network's address survive a save and a reload into
// a fresh storage and server, and a load reads a saved file without
// rewriting it.
func TestRouteReload(t *testing.T) {
	reload := func(root string) *server {
		t.Helper()
		st, err := openStorage(root)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.directory.Close() })
		loaded, err := loadSettings(st)
		if err != nil {
			t.Fatal(err)
		}
		s := testServer(t)
		s.storage, s.settings, s.controller.lastSaved = st, loaded, loaded
		return s
	}

	const fixture = `{"version":1,"bind":"0.0.0.0","idle":"0","discovery":true,"email":"synthetic@example.invalid","route":"lan","lan_address":"192.0.2.10:4450"}`
	s := testServer(t)
	path := filepath.Join(s.storage.root, "config.json")
	if err := s.storage.writeBytes("config.json", []byte(fixture)); err != nil {
		t.Fatal(err)
	}
	fresh := reload(s.storage.root)
	if fresh.settings.Route != routeLAN || fresh.settings.LANAddress != "192.0.2.10:4450" {
		t.Errorf("loaded %+v", fresh.settings)
	}
	if got, err := fresh.copyTarget(); got.Endpoint != "192.0.2.10:4450" || err != nil {
		t.Errorf("loaded endpoint = %q, %v", got, err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != fixture {
		t.Errorf("the load rewrote the file: %s, %v", b, err)
	}

	for _, tc := range []struct{ route, address, want string }{
		{routeLAN, "192.0.2.20", "192.0.2.20:445"},
		{routeAccount, "", "192.0.2.20:445"},
	} {
		if err := fresh.saveRoute(t.Context(), tc.route, tc.address); err != nil {
			t.Fatal(err)
		}
		again := reload(fresh.storage.root)
		if again.settings.Route != tc.route || again.settings.LANAddress != tc.want {
			t.Errorf("after saving %s and a reload: %+v", tc.route, again.settings)
		}
	}
}
