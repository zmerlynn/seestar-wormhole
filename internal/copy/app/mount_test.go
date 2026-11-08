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
	"sync/atomic"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"

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

// Browse is served under /browse/ for a same-origin frame, before a save
// too.
func TestMountBrowse(t *testing.T) {
	e := newMountEnv(t)
	if w := e.serve("GET", "/browse/", ""); w.Code != 200 {
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

// Copying that starts when the app opens waits for a target, through the
// account the connection, and starts once there is one; without the
// setting, or before a folder is saved, it doesn't start.
func TestMountStartsCopyingWhenReady(t *testing.T) {
	defer func(every time.Duration) { startEvery = every }(startEvery)
	startEvery = 5 * time.Millisecond
	open := func(set *Settings) (*Mount, func()) {
		var mu sync.Mutex
		ready := false
		m := NewMount(context.Background(), MountOptions{
			Addr: testAddr, StateDir: t.TempDir(), Version: "v9.9.9",
			Log: slog.New(slog.DiscardHandler),
			Target: func() (Target, error) {
				mu.Lock()
				defer mu.Unlock()
				if !ready {
					return Target{}, errors.New("Not connected")
				}
				return Target{Endpoint: testEndpoint}, nil
			},
			Dial:     (&dials{}).dial,
			Settings: set,
		})
		t.Cleanup(m.Quit)
		m.Start()
		return m, func() { mu.Lock(); ready = true; mu.Unlock() }
	}
	on := savedAt(t.TempDir())
	on.StartCopying = true
	m, connect := open(on)
	time.Sleep(50 * time.Millisecond)
	if m.s.copy.on() {
		t.Fatal("copying started before the connection")
	}
	connect()
	deadline := time.Now().Add(5 * time.Second)
	for !m.s.copy.on() {
		if time.Now().After(deadline) {
			t.Fatal("copying didn't start once connected")
		}
		time.Sleep(5 * time.Millisecond)
	}
	off := savedAt(t.TempDir())
	unset := DefaultSettings()
	unset.Folder, unset.StartCopying = "", true
	for _, set := range []*Settings{off, &unset, nil} {
		m, connect := open(set)
		connect()
		time.Sleep(50 * time.Millisecond)
		if m.s.copy.on() {
			t.Errorf("%+v: copying started", set)
		}
	}
}

// A start as the app opens that the target refused tries again at the
// next look, even when the target became ready right after the refusal:
// here the very look that refuses makes the next one ready.
func TestMountStartRetriesAClearedRefusal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var armed, ready atomic.Bool
		set := savedAt(t.TempDir())
		set.StartCopying = true
		m := NewMount(t.Context(), MountOptions{
			Addr: testAddr, StateDir: t.TempDir(), Settings: set, Log: slog.New(slog.DiscardHandler),
			Target: func() (Target, error) {
				if ready.Load() {
					return Target{Endpoint: testEndpoint, Device: "synthetic", Run: 1}, nil
				}
				if armed.Load() {
					ready.Store(true)
				}
				return Target{}, errors.New("Not connected")
			},
			Dial: func(context.Context, Target) (remote.Session, error) {
				return remote.NewFake(fstest.MapFS{"MyWorks/a.jpg": image("abc")}), nil
			},
		})
		defer m.Quit()
		armed.Store(true)
		m.Start()
		synctest.Wait()
		if !ready.Load() {
			t.Fatal("the start never looked for the target")
		}
		time.Sleep(2 * startEvery)
		synctest.Wait()
		if !m.s.copy.on() {
			t.Error("copying didn't start once the target was ready")
		}
	})
}

// A start from the page while copying waits to start as the app opens
// takes its place: once that start's single pass has ended, copying stays
// off, though the target is there by the next look.
func TestMountStartGivesWayToPage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var ready atomic.Bool
		var dialed atomic.Int32
		set := savedAt(t.TempDir())
		set.Once, set.StartCopying = true, true
		m := NewMount(t.Context(), MountOptions{
			Addr: testAddr, StateDir: t.TempDir(), Settings: set, Log: slog.New(slog.DiscardHandler),
			Target: func() (Target, error) {
				if !ready.Load() {
					return Target{}, errors.New("Not connected")
				}
				return Target{Endpoint: testEndpoint, Device: "synthetic", Run: 1}, nil
			},
			Dial: func(context.Context, Target) (remote.Session, error) {
				dialed.Add(1)
				return remote.NewFake(fstest.MapFS{"MyWorks/a.jpg": image("abc")}), nil
			},
		})
		defer m.Quit()
		m.Start()
		synctest.Wait() // no target yet; it looks again in a second
		ready.Store(true)
		if err := m.s.startCopying(false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if m.s.copy.on() || dialed.Load() != 1 {
			t.Fatalf("the page's pass: on %v after %d dials", m.s.copy.on(), dialed.Load())
		}
		time.Sleep(3 * startEvery)
		synctest.Wait()
		if m.s.copy.on() || dialed.Load() != 1 {
			t.Errorf("after the next looks: on %v after %d dials, want the page's one", m.s.copy.on(), dialed.Load())
		}
	})
}
