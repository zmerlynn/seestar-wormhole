package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

type mountEnv struct {
	t *testing.T
	m *Mount
	d *dials

	mu       sync.Mutex
	endpoint string
	err      error
	tested   []string
}

func newMountEnv(t *testing.T) *mountEnv {
	t.Helper()
	e := &mountEnv{t: t, d: &dials{}, endpoint: "127.0.0.1:4445"}
	e.m = NewMount(context.Background(), MountOptions{
		Addr: testAddr, StateDir: t.TempDir(), Version: "v9.9.9",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Target: func() (Target, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			return Target{Endpoint: e.endpoint}, e.err
		},
		Dial: e.d.dial,
		Test: func(ctx context.Context, endpoint string) ([]remote.Entry, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			e.tested = append(e.tested, endpoint)
			return []remote.Entry{{Path: "MyWorks", Kind: remote.Dir}}, nil
		},
	})
	t.Cleanup(e.m.Quit)
	return e
}

func (e *mountEnv) set(endpoint string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.endpoint, e.err = endpoint, err
}

// serve sends one request; the mounting server has made its checks.
func (e *mountEnv) serve(method, target, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Host = testAddr.String()
	if method == "POST" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.m.ServeHTTP(w, r)
	return w
}

// A Mount serves the app's API under /copy/api/ and keeps its settings in
// memory, with no address of its own.
func TestMountAPI(t *testing.T) {
	e := newMountEnv(t)
	if w := e.serve("GET", "/copy/api/status", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"set_up":false`) {
		t.Errorf("status before a save = %d %s", w.Code, w.Body.String())
	}
	if w := e.serve("POST", "/copy/api/settings", goodInput); w.Code != 200 {
		t.Fatalf("save without an address = %d %s", w.Code, w.Body.String())
	}
	if w := e.serve("GET", "/copy/api/status", ""); !strings.Contains(w.Body.String(), `"set_up":true`) {
		t.Errorf("status after a save = %s", w.Body.String())
	}
	if w := e.serve("POST", "/copy/api/test", `{"address":"192.0.2.30"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("test = %d %s", w.Code, w.Body.String())
	}
	if fmt.Sprint(e.tested) != "[192.0.2.30:445]" {
		t.Errorf("tested %v", e.tested)
	}
	for _, path := range []string{"/app/api/status", "/copy/api/nope", "/elsewhere"} {
		if w := e.serve("GET", path, ""); w.Code != 404 {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}
	if w := e.serve("GET", "/copy/api/settings", ""); w.Code != 405 {
		t.Errorf("GET settings = %d, want 405", w.Code)
	}
}

// Every start and every browse dial asks the endpoint again, and a start
// fails with the endpoint's reason when there is none.
func TestMountEndpoint(t *testing.T) {
	e := newMountEnv(t)
	if w := e.serve("POST", "/copy/api/settings", goodInput); w.Code != 200 {
		t.Fatal(w.Code)
	}
	e.set("", errors.New("SMB is off"))
	if w := e.serve("POST", "/copy/api/copy", `{"action":"start"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "SMB is off") {
		t.Errorf("start with no endpoint = %d %s", w.Code, w.Body.String())
	}
	if w := e.serve("POST", "/copy/api/copy", `{"action":"preview"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "SMB is off") {
		t.Errorf("preview with no endpoint = %d %s", w.Code, w.Body.String())
	}
	if w := e.serve("GET", "/browse/api/list", ""); w.Code == 200 {
		t.Errorf("browse with no endpoint = %d %s", w.Code, w.Body.String())
	}
	e.set("127.0.0.1:4446", nil)
	w := e.serve("GET", "/browse/api/list", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "at 127.0.0.1_4446") {
		t.Errorf("browse = %d %s", w.Code, w.Body.String())
	}
	if e.m.Busy() {
		t.Error("busy with nothing running")
	}
}

// Copying follows a selected telescope across runs and endpoints. With no
// device on either target, only the same endpoint in the same run is the
// same telescope: on this network always run 0, and through the account a
// reconnect may reuse the endpoint for another telescope.
func TestSameTelescope(t *testing.T) {
	for _, tc := range []struct {
		name string
		t, u Target
		want bool
	}{
		{"same address", Target{Endpoint: atA}, Target{Endpoint: atA}, true},
		{"other address", Target{Endpoint: atA}, Target{Endpoint: atB}, false},
		{"same device, next run, other endpoint", Target{Endpoint: atA, Device: "synthetic-a", Run: 1}, Target{Endpoint: atB, Device: "synthetic-a", Run: 2}, true},
		{"other device, same endpoint and run", Target{Endpoint: atA, Device: "synthetic-a", Run: 1}, Target{Endpoint: atA, Device: "synthetic-b", Run: 1}, false},
		{"device only on one", Target{Endpoint: atA, Run: 1}, Target{Endpoint: atA, Device: "synthetic-a", Run: 1}, false},
		{"no device, same run", Target{Endpoint: atA, Run: 1}, Target{Endpoint: atA, Run: 1}, true},
		{"no device, next run", Target{Endpoint: atA, Run: 1}, Target{Endpoint: atA, Run: 2}, false},
	} {
		if got := tc.t.sameTelescope(tc.u); got != tc.want {
			t.Errorf("%s: sameTelescope = %v", tc.name, got)
		}
	}
}

// Browse is served under /browse/ for a same-origin frame.
func TestMountBrowse(t *testing.T) {
	e := newMountEnv(t)
	if w := e.serve("GET", "/browse/", ""); w.Code != 404 {
		t.Errorf("browse before a save = %d", w.Code)
	}
	if w := e.serve("POST", "/copy/api/settings", goodInput); w.Code != 200 {
		t.Fatal(w.Code)
	}
	w := e.serve("GET", "/browse/", "")
	if w.Code != 200 || w.Header().Get("X-Frame-Options") != "SAMEORIGIN" ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Errorf("browse = %d %q", w.Code, w.Header())
	}
	if !strings.Contains(w.Body.String(), `src="app.js"`) {
		t.Error("browse's page does not load its script relative to /browse/")
	}
	if w := e.serve("GET", "/browse/app.js", ""); w.Code != 200 {
		t.Errorf("browse script = %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/browse/", nil)
	r.Host = "localhost:53818"
	rec := httptest.NewRecorder()
	e.m.ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Errorf("browse for another Host = %d, want 403", rec.Code)
	}
}

// Quit stops the controllers and can be called again.
func TestMountQuit(t *testing.T) {
	e := newMountEnv(t)
	e.m.Quit()
	e.m.Quit()
	e.serve("POST", "/copy/api/settings", goodInput)
	if w := e.serve("POST", "/copy/api/copy", `{"action":"start"}`); w.Code == 200 {
		t.Errorf("start after Quit = %d %s", w.Code, w.Body.String())
	}
}

func TestNormalizeAddressExported(t *testing.T) {
	if got, ok := NormalizeAddress("192.0.2.40"); !ok || got != "192.0.2.40:445" {
		t.Errorf("NormalizeAddress = %q, %v", got, ok)
	}
	if _, ok := NormalizeAddress("not an address!"); ok {
		t.Error("a bad address passed")
	}
}
