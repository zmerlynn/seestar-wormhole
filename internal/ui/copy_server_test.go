package ui

import (
	"context"
	"encoding/json/v2"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/localhttp"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
)

// The one policy every response carries, Browse's included: the page frames
// Browse, and only its own origin may frame either.
const pageCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; frame-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'"

// copyServer is testServer with copy mounted. Its copier reaches a fake
// share, and Test checks a fake that records what it was asked to reach.
func copyServer(t *testing.T) (*server, *[]string) {
	t.Helper()
	s := testServer(t)
	var tested []string
	s.copy = copyapp.NewMount(context.Background(), copyapp.MountOptions{
		Addr: netip.MustParseAddrPort(s.host), StateDir: t.TempDir(), Version: "synthetic",
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Target: s.copyTarget,
		Dial: func(context.Context, string) (remote.Session, error) {
			return remote.NewFake(fstest.MapFS{"MyWorks/a.jpg": {Data: []byte("a")}}), nil
		},
		Test: func(_ context.Context, endpoint string) ([]remote.Entry, error) {
			tested = append(tested, endpoint)
			if strings.HasPrefix(endpoint, "192.0.2.99:") {
				return nil, errors.New("synthetic unreachable")
			}
			return []remote.Entry{{Path: "MyWorks", Kind: remote.Dir}}, nil
		},
	})
	t.Cleanup(s.copy.Quit)
	return s, &tested
}

func serve(s *server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

// copySettings saves copy's settings for a folder, as the Copy tab will.
func copySettings(t *testing.T, s *server) {
	t.Helper()
	folder, _ := json.Marshal(filepath.Join(t.TempDir(), "Seestar"))
	body := `{"folder":` + string(folder) + `,"interval":"5","excludes":[],"includes":[],"skip_built_in":true,"start_copying":false}`
	if w := serve(s, request(s, "POST", "/copy/api/settings", body)); w.Code != 200 {
		t.Fatalf("copy settings = %d %s", w.Code, w.Body)
	}
}

func TestOneServerHeaders(t *testing.T) {
	s, _ := copyServer(t)
	copySettings(t, s)
	for _, path := range []string{"/", "/app.js", "/api/status", "/copy/api/status", "/nope", "/browse/", "/browse/api/list"} {
		w := serve(s, request(s, "GET", path, ""))
		for k, want := range map[string]string{"X-Frame-Options": "SAMEORIGIN", "Cross-Origin-Resource-Policy": "same-origin",
			"Content-Security-Policy": pageCSP, "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff", "Cache-Control": "no-store"} {
			if got := w.Header().Values(k); len(got) != 1 || got[0] != want {
				t.Errorf("%s (%d): %s = %q, want %q", path, w.Code, k, got, want)
			}
		}
	}
}

// copy's API takes the wormhole's token and makes the wormhole's checks.
func TestCopyAPIRejections(t *testing.T) {
	s, _ := copyServer(t)
	for _, tc := range []struct {
		name, method, path string
		mutate             func(*http.Request)
		code               int
	}{
		{"token", "GET", "/copy/api/status", func(r *http.Request) { r.Header.Del("X-Seestar-Token") }, 403},
		{"wrong token", "GET", "/copy/api/status", func(r *http.Request) { r.Header.Set("X-Seestar-Token", strings.Repeat("b", 64)) }, 403},
		{"copy's own token", "GET", "/copy/api/status", func(r *http.Request) {
			r.Header.Del("X-Seestar-Token")
			r.Header.Set("X-Seestar-Copy-Token", s.token)
		}, 403},
		{"host", "GET", "/copy/api/status", func(r *http.Request) { r.Host = "localhost:12345" }, 403},
		{"origin missing", "POST", "/copy/api/test", func(r *http.Request) { r.Header.Del("Origin") }, 403},
		{"origin foreign", "POST", "/copy/api/test", func(r *http.Request) { r.Header.Set("Origin", "https://example.invalid") }, 403},
		{"fetch metadata", "POST", "/copy/api/test", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		{"fetch metadata none", "POST", "/copy/api/test", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "none") }, 403},
		{"content type", "POST", "/copy/api/test", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"oversize", "POST", "/copy/api/settings", func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", localhttp.MaxJSON+1)))
		}, 413},
		{"method", "GET", "/copy/api/settings", nil, 405},
		{"status post", "POST", "/copy/api/status", nil, 405},
		{"unknown", "GET", "/copy/api/nope", nil, 404},
		{"standalone prefix", "GET", "/app/api/status", nil, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request(s, tc.method, tc.path, `{"address":"192.0.2.10"}`)
			if tc.mutate != nil {
				tc.mutate(r)
			}
			if w := serve(s, r); w.Code != tc.code {
				t.Fatalf("%d want %d: %s", w.Code, tc.code, w.Body)
			}
		})
	}
	if w := serve(s, request(s, "GET", "/copy/api/status", "")); w.Code != 200 {
		t.Errorf("status = %d", w.Code)
	}
	// Without copy mounted, neither prefix is served.
	plain := testServer(t)
	for _, path := range []string{"/copy/api/status", "/browse/"} {
		if w := serve(plain, request(plain, "GET", path, "")); w.Code != 404 {
			t.Errorf("%s without copy = %d", path, w.Code)
		}
	}
}

// Browse is served under /browse/ with no token, for the frame, and only
// for the exact Host and a same-origin fetch.
func TestBrowseFrame(t *testing.T) {
	s, _ := copyServer(t)
	frame := func(mutate func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://"+s.host+"/browse/", nil)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		if mutate != nil {
			mutate(r)
		}
		return serve(s, r)
	}
	if w := frame(nil); w.Code != 404 {
		t.Errorf("browse before copy settings = %d", w.Code)
	}
	copySettings(t, s)
	w := frame(nil)
	if w.Code != 200 || w.Header().Get("X-Frame-Options") != "SAMEORIGIN" || !strings.Contains(w.Body.String(), `src="app.js"`) {
		t.Errorf("browse = %d %q", w.Code, w.Header())
	}
	if w := frame(func(r *http.Request) { r.Host = "localhost:12345" }); w.Code != 403 {
		t.Errorf("browse for a foreign Host = %d", w.Code)
	}
	if w := frame(func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }); w.Code != 403 {
		t.Errorf("browse cross-site = %d", w.Code)
	}
	if w := frame(func(r *http.Request) { r.Method = "POST" }); w.Code < 400 {
		t.Errorf("browse POST = %d", w.Code)
	}
	// The listing reaches the telescope only by the saved route.
	list := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://"+s.host+"/browse/api/list", nil)
		return serve(s, r)
	}
	if w := list(); w.Code == 200 {
		t.Errorf("browse listing while not connected = %d %s", w.Code, w.Body)
	}
	if err := s.saveRoute(t.Context(), routeLAN, "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	if w := list(); w.Code != 200 || !strings.Contains(w.Body.String(), "MyWorks") {
		t.Errorf("browse listing on this network = %d %s", w.Code, w.Body)
	}
}

func TestCopyEndpoint(t *testing.T) {
	account := testSettings()
	lan := account
	lan.Route, lan.LANAddress = routeLAN, "192.0.2.10:4450"
	smbOff := account
	smbOff.Ports = []proxyrun.Port{{Local: 80, Device: 80}, {Local: 0, Device: 445}}
	moved := account
	moved.Ports = []proxyrun.Port{{Local: 5445, Device: 445}}
	local := account
	local.Bind, local.Discovery = "127.0.0.1", false
	specific := account
	specific.Bind = "192.0.2.5"
	for _, tc := range []struct {
		name       string
		saved, run Settings
		connected  bool
		want       string
		err        error
	}{
		{"lan", lan, Settings{}, false, "192.0.2.10:4450", nil},
		{"lan while the account runs", lan, account, true, "192.0.2.10:4450", nil},
		{"lan unset", Settings{Route: routeLAN}, Settings{}, false, "", errNoLANAddress},
		{"account not connected", account, account, false, "", errNotConnected},
		{"old file", Settings{}, account, false, "", errNotConnected},
		{"account", account, account, true, "127.0.0.1:4445", nil},
		{"account loopback bind", account, local, true, "127.0.0.1:4445", nil},
		{"account specific bind", account, specific, true, "192.0.2.5:4445", nil},
		{"account moved port", account, moved, true, "127.0.0.1:5445", nil},
		{"smb off", account, smbOff, true, "", errSMBOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := copyEndpointFor(tc.saved, tc.run, tc.connected)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("= %q, %v; want %q, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

// A run that has not reached Ready is not connected: the copier waits
// for the connection rather than starting it.
func TestCopyWaitsForTheConnection(t *testing.T) {
	s, _ := copyServer(t)
	release := make(chan struct{})
	s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status) error {
		select {
		case <-ctx.Done():
		case <-release:
		}
		return nil
	}
	if err := s.controller.start(testSettings(), testPassword); err != nil {
		t.Fatal(err)
	}
	defer func() { close(release); s.controller.quit() }()
	if _, err := s.copyTarget(); !errors.Is(err, errNotConnected) {
		t.Errorf("endpoint while connecting: %v", err)
	}
	copySettings(t, s)
	w := serve(s, request(s, "POST", "/copy/api/copy", `{"action":"start"}`))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "Not connected") {
		t.Errorf("start copying while connecting = %d %s", w.Code, w.Body)
	}
}

func TestRouteSave(t *testing.T) {
	s, _ := copyServer(t)
	path := filepath.Join(s.storage.root, "config.json")
	// A future member survives the save.
	if err := s.storage.writeBytes("config.json", []byte(`{"version":1,"bind":"0.0.0.0","idle":"0","discovery":true,"email":"synthetic@example.invalid","future":{"kept":true}}`)); err != nil {
		t.Fatal(err)
	}
	saved, err := loadSettings(s.storage)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Route != "" || saved.LANAddress != "" {
		t.Fatalf("an old file has a route: %+v", saved)
	}
	s.settings, s.controller.lastSaved = saved, saved
	if got, _ := copyEndpointFor(saved, Settings{}, false); got != "" {
		t.Error("an old file is not the account route")
	}
	post := func(body string) *httptest.ResponseRecorder {
		return serve(s, request(s, "POST", "/api/route", body))
	}
	for _, tc := range []struct{ body, code string }{
		{`{"settings":{"route":"lan","lan_address":""}}`, "lan_address"},
		{`{"settings":{"route":"lan","lan_address":"not an address!"}}`, "lan_address"},
	} {
		w := post(tc.body)
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Errorf("%s = %d %s", tc.body, w.Code, w.Body)
		}
	}
	for _, body := range []string{`{"settings":{"route":"usb"}}`, `{"settings":{}}`, `{}`} {
		if w := post(body); w.Code != 400 {
			t.Errorf("%s = %d", body, w.Code)
		}
	}
	if w := post(`{"settings":{"route":"lan","lan_address":" 192.0.2.10 "}}`); w.Code != 200 {
		t.Fatalf("save lan = %d %s", w.Code, w.Body)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"route":"lan"`, `"lan_address":"192.0.2.10:445"`, `"future":{"kept":true}`, `"email":"synthetic@example.invalid"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("config.json lacks %s: %s", want, b)
		}
	}
	status := serve(s, request(s, "GET", "/api/status", ""))
	if !strings.Contains(status.Body.String(), `"route":"lan"`) || !strings.Contains(status.Body.String(), `"copy_busy":false`) {
		t.Errorf("status = %s", status.Body)
	}
	if got, err := s.copyTarget(); got.Endpoint != "192.0.2.10:445" || err != nil {
		t.Errorf("endpoint = %q, %v", got, err)
	}
	// The account route keeps the address for a switch back.
	if w := post(`{"settings":{"route":"account","lan_address":"192.0.2.20"}}`); w.Code != 200 {
		t.Fatalf("save account = %d %s", w.Code, w.Body)
	}
	if s.settings.Route != routeAccount || s.settings.LANAddress != "192.0.2.10:445" {
		t.Errorf("after the account route: %+v", s.settings)
	}
	// A save of the password keeps the route; Connect chooses the account.
	if err := s.saveRoute(t.Context(), routeLAN, "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(input{Settings: testSettings(), Password: testPassword})
	if w := serve(s, request(s, "POST", "/api/password/save", string(in))); w.Code != 200 {
		t.Fatalf("password save = %d %s", w.Code, w.Body)
	}
	if s.settings.Route != routeLAN {
		t.Errorf("a password save changed the route: %+v", s.settings)
	}
}

// The choice is locked while the account route runs and while copying or
// deleting runs.
func TestRouteLocked(t *testing.T) {
	s, _ := copyServer(t)
	release := make(chan struct{})
	s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status) error {
		select {
		case <-ctx.Done():
		case <-release:
		}
		return nil
	}
	in, _ := json.Marshal(input{Settings: testSettings(), Password: testPassword})
	if w := serve(s, request(s, "POST", "/api/start", string(in))); w.Code != 200 {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	if s.settings.Route != routeAccount {
		t.Errorf("Connect did not choose the account route: %+v", s.settings)
	}
	w := serve(s, request(s, "POST", "/api/route", `{"settings":{"route":"lan","lan_address":"192.0.2.10"}}`))
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"route_locked"`) {
		t.Errorf("route while connecting = %d %s", w.Code, w.Body)
	}
	close(release)
	s.controller.quit()
	if s.settings.Route == routeLAN {
		t.Error("a locked save changed the route")
	}

	// Copying on this network locks it too.
	s, _ = copyServer(t)
	if err := s.saveRoute(t.Context(), routeLAN, "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	copySettings(t, s)
	if w := serve(s, request(s, "POST", "/copy/api/copy", `{"action":"start"}`)); w.Code != 200 {
		t.Fatalf("start copying = %d %s", w.Code, w.Body)
	}
	if !s.copy.Busy() {
		t.Fatal("copying is not busy")
	}
	if !strings.Contains(serve(s, request(s, "GET", "/api/status", "")).Body.String(), `"copy_busy":true`) {
		t.Error("status does not say copying is busy")
	}
	w = serve(s, request(s, "POST", "/api/route", `{"settings":{"route":"account"}}`))
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"route_locked"`) {
		t.Errorf("route while copying = %d %s", w.Code, w.Body)
	}
	if w := serve(s, request(s, "POST", "/copy/api/copy", `{"action":"stop"}`)); w.Code != 200 {
		t.Fatalf("stop copying = %d", w.Code)
	}
}

// LAN Test is copy's Test, reached through the one server and token.
func TestLANTest(t *testing.T) {
	s, tested := copyServer(t)
	w := serve(s, request(s, "POST", "/copy/api/test", `{"address":"192.0.2.10"}`))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) || !strings.Contains(w.Body.String(), `"address":"192.0.2.10:445"`) {
		t.Errorf("test = %d %s", w.Code, w.Body)
	}
	w = serve(s, request(s, "POST", "/copy/api/test", `{"address":"192.0.2.99:4450"}`))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Errorf("failed test = %d %s", w.Code, w.Body)
	}
	w = serve(s, request(s, "POST", "/copy/api/test", `{"address":"not an address!"}`))
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"address"`) {
		t.Errorf("bad address = %d %s", w.Code, w.Body)
	}
	if len(*tested) != 2 || (*tested)[0] != "192.0.2.10:445" || (*tested)[1] != "192.0.2.99:4450" {
		t.Errorf("tested %v", *tested)
	}
	// Test saves nothing.
	if s.settings.Route != "" && s.settings.Route != routeAccount {
		t.Errorf("Test changed the route: %+v", s.settings)
	}
}

// closeSession counts its Close in the open sessions.
type closeSession struct {
	remote.Session
	open *atomic.Int64
}

func (c closeSession) Close() error {
	c.open.Add(-1)
	return c.Session.Close()
}

// Quit closes Browse's session, then stops the copy controllers, then the
// proxy, and only then the HTTP server: when the proxy's run ends, no
// device session is open and copying is off, and the server has not
// begun to shut down.
func TestQuitOrder(t *testing.T) {
	s := testServer(t)
	var open atomic.Int64
	s.copy = copyapp.NewMount(context.Background(), copyapp.MountOptions{
		Addr: netip.MustParseAddrPort(s.host), StateDir: t.TempDir(), Version: "synthetic",
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Target: s.copyTarget,
		Dial: func(context.Context, string) (remote.Session, error) {
			open.Add(1)
			return closeSession{remote.NewFake(fstest.MapFS{"MyWorks/a.jpg": {Data: []byte("a")}}), &open}, nil
		},
		Test: func(context.Context, string) ([]remote.Entry, error) {
			return []remote.Entry{{Path: "MyWorks", Kind: remote.Dir}}, nil
		},
	})
	t.Cleanup(s.copy.Quit)
	if err := s.saveRoute(t.Context(), routeLAN, "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	copySettings(t, s)
	type atStop struct {
		open      int64
		copying   bool
		httpEnded bool
	}
	stopped := make(chan atStop, 1)
	running := make(chan struct{})
	s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status) error {
		close(running)
		<-ctx.Done()
		ended := false
		select {
		case <-s.quit:
			ended = true
		default:
		}
		stopped <- atStop{open.Load(), s.copy.Busy(), ended}
		return nil
	}
	if err := s.controller.start(testSettings(), testPassword); err != nil {
		t.Fatal(err)
	}
	<-running
	if w := serve(s, request(s, "GET", "/browse/api/list?path=MyWorks", "")); w.Code != 200 {
		t.Fatalf("browse = %d %s", w.Code, w.Body)
	}
	if w := serve(s, request(s, "POST", "/copy/api/copy", `{"action":"start"}`)); w.Code != 200 {
		t.Fatalf("start copying = %d %s", w.Code, w.Body)
	}
	if n := open.Load(); n < 1 || !s.copy.Busy() {
		t.Fatalf("before Quit: %d sessions open, copying %v", n, s.copy.Busy())
	}
	if w := serve(s, request(s, "POST", "/api/quit", "{}")); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if got := <-stopped; got.open != 0 || got.copying || got.httpEnded {
		t.Errorf("when the proxy stopped: %+v, want no session, copying off and the server up", got)
	}
	select {
	case <-s.quit:
	default:
		t.Error("Quit did not go on to shut the server down")
	}
}

// Quit stops the copier before the proxy.
func TestQuitStopsCopy(t *testing.T) {
	s, _ := copyServer(t)
	if err := s.saveRoute(t.Context(), routeLAN, "192.0.2.10"); err != nil {
		t.Fatal(err)
	}
	copySettings(t, s)
	if w := serve(s, request(s, "POST", "/copy/api/copy", `{"action":"start"}`)); w.Code != 200 {
		t.Fatalf("start copying = %d %s", w.Code, w.Body)
	}
	if w := serve(s, request(s, "POST", "/api/quit", "{}")); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if s.copy.Busy() {
		t.Error("copying is still on after Quit")
	}
}
