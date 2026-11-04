package browse

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

var t0 = time.Date(2026, 11, 1, 3, 10, 0, 100000000, time.UTC)

// fitData is the 200-byte X.fit: byte i is i.
var fitData = func() []byte {
	b := make([]byte, 200)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}()

// logText is a log file's text, with UTF-8 as the telescope writes it.
const logText = "Target RA:22h25m4s DEC:-90\u00b00'0\"\n"

func fixture() fstest.MapFS {
	file := func(data []byte) *fstest.MapFile { return &fstest.MapFile{Data: data, ModTime: t0} }
	dir := func() *fstest.MapFile { return &fstest.MapFile{Mode: fs.ModeDir | 0o755, ModTime: t0} }
	return fstest.MapFS{
		"MyWorks":                            dir(),
		"MyWorks/M 31":                       dir(),
		"MyWorks/M 31/sub":                   dir(),
		"MyWorks/M 31/X.fit":                 file(fitData),
		"MyWorks/M 31/X.jpg":                 file([]byte("jpeg")),
		"MyWorks/M 31/X_thn.jpg":             file([]byte("thn")),
		"MyWorks/M 31/link":                  {Mode: fs.ModeSymlink, Data: []byte("X.fit"), ModTime: t0},
		"MyWorks/log":                        dir(),
		"MyWorks/log/Autorun_Log_1.txt":      file([]byte(logText)),
		"MyWorks/log/b.FITS":                 file([]byte("SIMPLE")),
		"MyWorks/odd":                        dir(),
		"MyWorks/odd/a b#%?&+\"<script>.jpg": file([]byte("x")),
		"MyWorks/odd/\u00e9.fit":             file([]byte("nfc")),
		"MyWorks/odd/e\u0301.fit":            file([]byte("nfd")),
		"MyWorks/odd/%2e%2e":                 file([]byte("dots")),
	}
}

type env struct {
	d     *dialer
	p     *Sessions
	h     *Handler
	addr  netip.AddrPort
	loads atomic.Int32
	logs  *logBuffer
}

type envOptions struct {
	addr       string // the listener's; 127.0.0.1:53817 if empty
	dest       bool   // statuses from records
	records    map[string]state.Record
	recordsErr error
	dialErr    error
	setup      func(*spy)
	extra      fstest.MapFS // added to the fixture
	inApp      bool
	selector   engine.Selector
}

func newEnv(t *testing.T, o envOptions) *env {
	t.Helper()
	if o.addr == "" {
		o.addr = "127.0.0.1:53817"
	}
	log, logs := newLog()
	fsys := fixture()
	maps.Copy(fsys, o.extra)
	e := &env{d: &dialer{fsys: fsys, err: o.dialErr, setup: o.setup}, addr: netip.MustParseAddrPort(o.addr), logs: logs}
	e.p = NewSessions(context.Background(), e.d.dial, log)
	t.Cleanup(e.p.Close)
	cfg := Config{Addr: e.addr, Endpoint: "127.0.0.1:4445", Sessions: e.p, Log: log, InApp: o.inApp, Selector: o.selector}
	if o.dest {
		cfg.Records = func() (map[string]state.Record, error) {
			e.loads.Add(1)
			return o.records, o.recordsErr
		}
	}
	e.h = NewHandler(cfg)
	return e
}

// serve sends one request. headers are name and value pairs; "Host" sets
// the Host, which is otherwise the listener's address.
func (e *env) serve(method, target string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.Host = e.addr.String()
	for i := 0; i < len(headers); i += 2 {
		if headers[i] == "Host" {
			r.Host = headers[i+1]
		} else {
			r.Header.Set(headers[i], headers[i+1])
		}
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

// routes is one request to every route.
var routes = []string{"/", "/app.js", "/style.css", "/icon.svg", "/api/list?path=MyWorks", "/file?path=MyWorks%2FM%2031%2FX.jpg"}

func TestHostCheck(t *testing.T) {
	tests := []struct {
		addr   string
		refuse []string
		allow  []string
	}{
		{
			addr:   "127.0.0.1:53817",
			refuse: []string{"localhost:53817", "127.0.0.1:53818", "127.0.0.2:53817", "evil.example:53817", "127.0.0.1", "", "[::ffff:127.0.0.1]:53817", "127.0.0.1:053817", "127.0.0.1:53817."},
			allow:  []string{"127.0.0.1:53817"},
		},
		{
			addr:   "192.168.1.5:53817",
			refuse: []string{"127.0.0.1:53817", "192.168.1.5", "nas.local:53817"},
			allow:  []string{"192.168.1.5:53817"},
		},
		{
			addr:   "[::1]:53817",
			refuse: []string{"127.0.0.1:53817", "[::1]", "localhost:53817", "::1:53817"},
			allow:  []string{"[::1]:53817"},
		},
		{
			addr:   "0.0.0.0:53817",
			refuse: []string{"192.168.1.5:53818", "evil.example:53817", "localhost:53817", "nas.local:53817", "192.168.1.5", "[::1]", ""},
			allow:  []string{"127.0.0.1:53817", "192.168.1.5:53817", "[::1]:53817"},
		},
		{
			addr:   "[::]:53817",
			refuse: []string{"192.168.1.5:53818", "evil.example:53817", "localhost:53817", "127.0.0.1", ""},
			allow:  []string{"127.0.0.1:53817", "192.168.1.5:53817", "[::1]:53817"},
		},
		// A Host without a port is port 80.
		{addr: "127.0.0.1:80", refuse: []string{"localhost", "127.0.0.2"}, allow: []string{"127.0.0.1", "127.0.0.1:80"}},
		{addr: "[::1]:80", refuse: []string{"::1", "localhost"}, allow: []string{"[::1]", "[::1]:80"}},
		{addr: "0.0.0.0:80", refuse: []string{"localhost", "evil.example"}, allow: []string{"192.168.1.5", "[::1]", "127.0.0.1:80"}},
		{addr: "[::]:80", refuse: []string{"nas.local"}, allow: []string{"127.0.0.1", "[fd00::5]"}},
	}
	for _, tt := range tests {
		e := newEnv(t, envOptions{addr: tt.addr, dest: true})
		for _, route := range routes {
			for _, host := range tt.refuse {
				if w := e.serve("GET", route, "Host", host); w.Code != http.StatusForbidden {
					t.Errorf("listening on %s: Host %q, %s = %d, want 403", tt.addr, host, route, w.Code)
				}
			}
		}
		if e.d.dials() != 0 || e.loads.Load() != 0 {
			t.Errorf("listening on %s: refused hosts made %d dials and %d state loads", tt.addr, e.d.dials(), e.loads.Load())
		}
		for _, host := range tt.allow {
			if w := e.serve("GET", "/api/list?path=MyWorks", "Host", host); w.Code != http.StatusOK {
				t.Errorf("listening on %s: Host %q = %d, want 200", tt.addr, host, w.Code)
			}
		}
	}
}

func TestFetchMetadata(t *testing.T) {
	e := newEnv(t, envOptions{dest: true})
	for _, route := range routes {
		for _, site := range []string{"cross-site", "same-site", "bogus"} {
			if w := e.serve("GET", route, "Sec-Fetch-Site", site); w.Code != http.StatusForbidden {
				t.Errorf("Sec-Fetch-Site %s, %s = %d, want 403", site, route, w.Code)
			}
		}
	}
	if e.d.dials() != 0 || e.loads.Load() != 0 {
		t.Errorf("refused requests made %d dials and %d state loads", e.d.dials(), e.loads.Load())
	}
	for _, route := range routes {
		for _, site := range []string{"same-origin", "none", ""} {
			if w := e.serve("GET", route, "Sec-Fetch-Site", site); w.Code != http.StatusOK {
				t.Errorf("Sec-Fetch-Site %q, %s = %d, want 200", site, route, w.Code)
			}
		}
	}
}

func TestMethods(t *testing.T) {
	e := newEnv(t, envOptions{dest: true})
	for _, route := range append(routes, "/nowhere") {
		for _, m := range []string{"POST", "PUT", "DELETE", "OPTIONS", "PATCH"} {
			w := e.serve(m, route)
			if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("%s %s = %d, Allow %q; want 405 and GET, HEAD", m, route, w.Code, w.Header().Get("Allow"))
			}
		}
	}
	if e.d.dials() != 0 || e.loads.Load() != 0 {
		t.Errorf("refused methods made %d dials and %d state loads", e.d.dials(), e.loads.Load())
	}
}

func TestUnknownRoutes(t *testing.T) {
	e := newEnv(t, envOptions{dest: true})
	for _, route := range []string{"/favicon.ico", "/index.html", "/api/list/", "/api/", "/file/MyWorks", "/web/app.js", "//app.js"} {
		if w := e.serve("GET", route); w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", route, w.Code)
		}
	}
	if e.d.dials() != 0 || e.loads.Load() != 0 {
		t.Errorf("unknown routes made %d dials and %d state loads", e.d.dials(), e.loads.Load())
	}
}

func TestBadQueries(t *testing.T) {
	e := newEnv(t, envOptions{dest: true})
	// Open the session first, so a check that let a request through would
	// show up as a call on it.
	if w := e.serve("GET", "/api/list"); w.Code != http.StatusOK {
		t.Fatalf("root listing = %d", w.Code)
	}
	s := e.d.session(0)
	tests := []struct{ query, code string }{
		{"path=%ZZ", "bad_query"},
		{"path=a;b", "bad_query"},
		{"path=MyWorks&path=%ZZ", "bad_query"},
		{"path=MyWorks&path=MyWorks", "bad_query"},
		{"path=MyWorks&download=1&download=1", "bad_query"},
		{"path=%00", "bad_path"},
		{"path=a%00b", "bad_path"},
		{"path=%FF", "bad_path"},
		{"path=.", "bad_path"},
		{"path=%2FMyWorks", "bad_path"},
		{"path=MyWorks%2F", "bad_path"},
		{"path=MyWorks%2F%2FM%2031", "bad_path"},
		{"path=MyWorks%2F.%2FM%2031", "bad_path"},
		{"path=MyWorks%2F..%2FMyWorks", "bad_path"},
		{"path=..", "bad_path"},
		{"path=MyWorks%5CM%2031", "bad_path"},
	}
	for _, route := range []string{"/api/list", "/file"} {
		for _, tt := range tests {
			w := e.serve("GET", route+"?"+tt.query)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tt.code) && route == "/api/list" {
				t.Errorf("%s?%s = %d %q, want 400 %s", route, tt.query, w.Code, w.Body, tt.code)
			}
		}
		// An empty path is the root for a listing, never a file.
		if w := e.serve("GET", route+"?path="); (route == "/file") != (w.Code == http.StatusBadRequest) {
			t.Errorf("%s?path= = %d", route, w.Code)
		}
	}
	if s.n("ReadDir") != 2 || s.n("Open") != 0 || e.d.dials() != 1 {
		t.Errorf("bad queries reached the device: %d listings (want 2), %d opens, %d dials", s.n("ReadDir"), s.n("Open"), e.d.dials())
	}
	// A literal %2e%2e is a name, decoded once and never cleaned.
	w := e.serve("GET", "/file?path=MyWorks%2Fodd%2F%252e%252e")
	if w.Code != http.StatusOK || w.Body.String() != "dots" || s.n("Open:MyWorks/odd/%2e%2e") != 1 {
		t.Errorf("literal %%2e%%2e = %d %q", w.Code, w.Body)
	}
}

func TestHeaders(t *testing.T) {
	ok := newEnv(t, envOptions{dest: true})
	badState := newEnv(t, envOptions{dest: true, recordsErr: errors.New("bad state")})
	noDevice := newEnv(t, envOptions{dialErr: errors.New("no route")})
	tests := []struct {
		e      *env
		method string
		target string
		header []string
		code   int
	}{
		{ok, "GET", "/", nil, 200},
		{ok, "GET", "/app.js", nil, 200},
		{ok, "GET", "/style.css", nil, 200},
		{ok, "GET", "/icon.svg", nil, 200},
		{ok, "GET", "/api/list?path=MyWorks", nil, 200},
		{ok, "GET", "/file?path=MyWorks%2FM%2031%2FX.jpg", nil, 200},
		{ok, "HEAD", "/file?path=MyWorks%2FM%2031%2FX.jpg", nil, 200},
		{ok, "GET", "/file?path=MyWorks%2FM%2031%2FX.fit", []string{"Range", "bytes=0-9"}, 206},
		{ok, "GET", "/file?path=MyWorks%2FM%2031%2FX.fit", []string{"If-Modified-Since", t0.Format(http.TimeFormat)}, 304},
		{ok, "GET", "/file?path=MyWorks%2FM%2031%2FX.fit", []string{"Range", "bytes=500-"}, 416},
		{ok, "GET", "/", []string{"Host", "evil.example:53817"}, 403},
		{ok, "GET", "/file?path=x", []string{"Host", "evil.example:53817"}, 403},
		{ok, "GET", "/api/list", []string{"Sec-Fetch-Site", "cross-site"}, 403},
		{ok, "POST", "/file?path=x", nil, 405},
		{ok, "GET", "/nowhere", nil, 404},
		{ok, "GET", "/api/list?path=%ZZ", nil, 400},
		{ok, "GET", "/file?path=%ZZ", nil, 400},
		{ok, "GET", "/api/list?path=MyWorks%2FM%2031%2FX.fit", nil, 400},
		{ok, "GET", "/file?path=MyWorks", nil, 400},
		{ok, "GET", "/api/list?path=nothing", nil, 404},
		{ok, "GET", "/file?path=nothing", nil, 404},
		{badState, "GET", "/api/list", nil, 500},
		{noDevice, "GET", "/api/list", nil, 502},
		{noDevice, "GET", "/file?path=x", nil, 502},
	}
	for _, tt := range tests {
		w := tt.e.serve(tt.method, tt.target, tt.header...)
		if w.Code != tt.code {
			t.Errorf("%s %s %q = %d, want %d", tt.method, tt.target, tt.header, w.Code, tt.code)
		}
		want := map[string]string{
			"Content-Security-Policy":      csp,
			"Cross-Origin-Resource-Policy": "same-origin",
			"X-Frame-Options":              "DENY",
			"Referrer-Policy":              "no-referrer",
			"X-Content-Type-Options":       "nosniff",
			"Cache-Control":                "no-store",
		}
		if strings.HasPrefix(tt.target, "/file") {
			want["Content-Security-Policy"] = csp + "; sandbox"
		}
		if tt.code == http.StatusRequestedRangeNotSatisfiable {
			// http.ServeContent's own 416 and 500 answers drop
			// Cache-Control. That is accepted: they carry no file data, and
			// 416 and 500 are not heuristically cacheable.
			delete(want, "Cache-Control")
		}
		for k, v := range want {
			if got := w.Header().Values(k); len(got) != 1 || got[0] != v {
				t.Errorf("%s %s (%d): %s = %q, want %q", tt.method, tt.target, w.Code, k, got, v)
			}
		}
		for k := range w.Header() {
			if strings.HasPrefix(k, "Access-Control-") {
				t.Errorf("%s %s: has %s", tt.method, tt.target, k)
			}
		}
	}
}

func TestInApp(t *testing.T) {
	app := newEnv(t, envOptions{inApp: true})
	cli := newEnv(t, envOptions{})
	for _, route := range routes {
		w := app.serve("GET", route)
		policy := cspInApp
		if strings.HasPrefix(route, "/file") {
			policy += "; sandbox"
		}
		if got := w.Header().Get("Content-Security-Policy"); got != policy {
			t.Errorf("in app, %s: CSP = %q, want %q", route, got, policy)
		}
		if got := w.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
			t.Errorf("in app, %s: X-Frame-Options = %q, want SAMEORIGIN", route, got)
		}
	}
	if !strings.Contains(cspInApp, "frame-ancestors 'self';") || strings.Count(cspInApp, "frame-ancestors") != 1 {
		t.Errorf("in-app CSP %q must allow framing by its own origin only", cspInApp)
	}
	if strings.Replace(cspInApp, "'self'; base-uri", "'none'; base-uri", 1) != csp {
		t.Errorf("in-app CSP %q differs from %q in more than frame-ancestors", cspInApp, csp)
	}
	if page := app.serve("GET", "/").Body.String(); !strings.Contains(page, `<html lang="en" class="in-app">`) {
		t.Error("in app, index.html lacks the in-app class")
	}
	if page := cli.serve("GET", "/").Body.String(); strings.Contains(page, "in-app") {
		t.Error("outside the app, index.html has the in-app class")
	}
	for _, f := range []string{"web/index.html", "web/style.css", "web/app.js"} {
		b, _ := assets.ReadFile(f)
		if f == "web/index.html" && !strings.Contains(string(b), `<html lang="en">`) {
			t.Errorf("%s lacks the <html> tag the in-app class is added to", f)
		}
		if f != "web/index.html" && !strings.Contains(string(b), "in-app") {
			t.Errorf("%s does not handle the in-app class", f)
		}
	}
}

func TestStatic(t *testing.T) {
	e := newEnv(t, envOptions{})
	for route, typ := range map[string]string{
		"/":              "text/html; charset=utf-8",
		"/?path=MyWorks": "text/html; charset=utf-8",
		"/app.js":        "text/javascript; charset=utf-8",
		"/style.css":     "text/css; charset=utf-8",
		"/icon.svg":      "image/svg+xml",
	} {
		w := e.serve("GET", route)
		if w.Code != 200 || w.Header().Get("Content-Type") != typ || w.Body.Len() == 0 {
			t.Errorf("%s = %d, %q, %d bytes; want 200 and %q", route, w.Code, w.Header().Get("Content-Type"), w.Body.Len(), typ)
		}
	}
	page := e.serve("GET", "/").Body.String()
	for _, bad := range []string{"<script>", "style=", "<style", "http://", "https://", "javascript:", "onclick", "onload"} {
		if strings.Contains(page, bad) {
			t.Errorf("index.html holds %q", bad)
		}
	}
	if strings.Count(page, "<script") != 1 || !strings.Contains(page, `<script type="module" src="/app.js"></script>`) {
		t.Error("index.html must load app.js as its only script")
	}
	if !strings.Contains(page, `<link rel="icon" type="image/svg+xml" href="/icon.svg">`) {
		t.Error("index.html must link icon.svg as its icon")
	}
	size := 0
	for _, f := range []string{"web/index.html", "web/app.js", "web/style.css", "web/icon.svg"} {
		b, err := assets.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		size += len(b)
		if strings.Contains(string(b), "innerHTML") {
			t.Errorf("%s uses innerHTML", f)
		}
	}
	if size > 15000 {
		t.Errorf("the page is %d bytes, over 15 KB", size)
	}
	if e.d.dials() != 0 {
		t.Errorf("the page made %d dials", e.d.dials())
	}
}
