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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// routeEnv is a server with copy mounted over a share at each endpoint:
// a folder holding MyWorks/at_<host>.jpg. Its fakes record each endpoint
// reached and can hold a dial, a delete's remover or a Test.
type routeEnv struct {
	t      *testing.T
	s      *server
	shares string
	folder string

	mu         sync.Mutex
	dialed     []string
	holdDial   chan struct{} // if not nil, a dial waits for it
	holdRemove chan struct{} // if not nil, a remover waits for it
	holdTest   chan struct{} // if not nil, a Test waits for it
	tested     chan struct{} // if not nil, a Test signals it on entry
}

func newRouteEnv(t *testing.T) *routeEnv {
	t.Helper()
	base := t.TempDir()
	e := &routeEnv{t: t, s: testServer(t), shares: filepath.Join(base, "shares"), folder: filepath.Join(base, "Seestar")}
	e.s.copy = copyapp.NewMount(context.Background(), copyapp.MountOptions{
		Addr: netip.MustParseAddrPort(e.s.host), StateDir: filepath.Join(base, "state"), Version: "synthetic",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Endpoint: e.s.copyEndpoint,
		Dial: func(ctx context.Context, endpoint string) (remote.Session, error) {
			if err := e.wait(ctx, &e.holdDial, nil); err != nil {
				return nil, err
			}
			e.mu.Lock()
			e.dialed = append(e.dialed, endpoint)
			e.mu.Unlock()
			return e.share(endpoint)
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
			if err := e.wait(ctx, &e.holdRemove, nil); err != nil {
				return nil, err
			}
			return e.share(endpoint)
		},
	})
	t.Cleanup(e.s.copy.Quit)
	return e
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

// share is the share at endpoint, made on first use with a file older
// than any days option.
func (e *routeEnv) share(endpoint string) (*remote.FakeDir, error) {
	host, _, _ := strings.Cut(endpoint, ":")
	dir := filepath.Join(e.shares, host)
	name := filepath.Join(dir, "MyWorks", "at_"+host+".jpg")
	if _, err := os.Stat(name); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(name, []byte("synthetic"), 0o644); err != nil {
			return nil, err
		}
		old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
		if err := os.Chtimes(name, old, old); err != nil {
			return nil, err
		}
	}
	return remote.NewFakeDir(dir)
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
	if code, body := e.post("/copy/api/settings", `{"folder":`+string(folder)+`,"interval":"5","excludes":[],"includes":[],"skip_built_in":true,"start_copying":false}`); code != 200 {
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
// another address, a save of the account route, and Connect.
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
			tc.change(e)
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
	<-tested
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
