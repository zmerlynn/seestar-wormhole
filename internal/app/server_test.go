package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hirochachacha/go-smb2/v2/x/protocol"

	"github.com/zmerlynn/seestar-copy/internal/remote"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var testAddr = netip.MustParseAddrPort("127.0.0.1:53818")

// dials records the browse sessions opened, by endpoint.
type dials struct {
	mu        sync.Mutex
	endpoints []string
	sessions  []*remote.Fake
}

func (d *dials) dial(ctx context.Context, endpoint string) (remote.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Each address has its own share: one folder named for it.
	f := remote.NewFake(fstest.MapFS{"at " + strings.ReplaceAll(endpoint, ":", "_") + "/x.txt": {Data: []byte("x")}})
	d.endpoints = append(d.endpoints, endpoint)
	d.sessions = append(d.sessions, f)
	return f, nil
}

type appEnv struct {
	t    *testing.T
	s    *server
	d    *dials
	path string
}

func newAppEnv(t *testing.T, fileData string) *appEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.json")
	if fileData != "" {
		writeFile(t, path, fileData)
	}
	file, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &appEnv{t: t, d: &dials{}, path: path}
	e.s = newServer(context.Background(), testAddr, testToken, "v9.9.9", t.TempDir(), file, log)
	e.s.dial = e.d.dial
	e.s.test = func(ctx context.Context, endpoint string) ([]remote.Entry, error) {
		return []remote.Entry{{Path: "MyWorks", Kind: remote.Dir}}, nil
	}
	e.s.startBrowse()
	t.Cleanup(e.s.close)
	t.Cleanup(e.s.copy.quit)
	t.Cleanup(e.s.free.quit)
	return e
}

// serve sends one request. headers are name and value pairs; "Host" sets
// the Host, which is otherwise the listener's address.
func (e *appEnv) serve(method, target, body string, headers ...string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r.Host = testAddr.String()
	for i := 0; i < len(headers); i += 2 {
		if headers[i] == "Host" {
			r.Host = headers[i+1]
		} else {
			r.Header.Set(headers[i], headers[i+1])
		}
	}
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	return w
}

// post sends a POST as the page does.
func (e *appEnv) post(path, body string) *httptest.ResponseRecorder {
	return e.serve("POST", path, body, tokenHeader, testToken, "Origin", "http://"+testAddr.String(),
		"Sec-Fetch-Site", "same-origin", "Content-Type", "application/json")
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%d %q: %v", w.Code, w.Body.String(), err)
	}
	return v
}

// abs makes a slash path absolute on this OS: on Windows, "/x" is not.
func abs(p string) string {
	a, err := filepath.Abs(filepath.FromSlash(p))
	if err != nil {
		panic(err)
	}
	return a
}

// quote is s as a JSON string.
func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

var goodInput = `{"address":"192.0.2.10","folder":` + quote(abs("/data/Seestar")) + `,"interval":"5","excludes":["Video","","/Preview"],"includes":[],"skip_built_in":true,"start_copying":false}`

func TestAPIChecks(t *testing.T) {
	e := newAppEnv(t, "")
	origin := "http://" + testAddr.String()
	ok := []string{tokenHeader, testToken, "Origin", origin, "Sec-Fetch-Site", "same-origin", "Content-Type", "application/json"}
	with := func(name, value string) []string {
		h := append([]string(nil), ok...)
		for i := 0; i < len(h); i += 2 {
			if h[i] == name {
				h[i+1] = value
				return h
			}
		}
		return append(h, name, value)
	}
	without := func(name string) []string {
		var h []string
		for i := 0; i < len(ok); i += 2 {
			if ok[i] != name {
				h = append(h, ok[i], ok[i+1])
			}
		}
		return h
	}
	tests := []struct {
		name   string
		method string
		path   string
		header []string
		code   int
	}{
		{"status", "GET", "/app/api/status", ok, 200},
		{"status without token", "GET", "/app/api/status", nil, 403},
		{"status wrong token", "GET", "/app/api/status", with(tokenHeader, testToken[1:]+"0"), 403},
		{"status short token", "GET", "/app/api/status", with(tokenHeader, testToken[:10]), 403},
		{"status cross-site", "GET", "/app/api/status", with("Sec-Fetch-Site", "cross-site"), 403},
		{"status same-site", "GET", "/app/api/status", with("Sec-Fetch-Site", "same-site"), 403},
		{"status other host", "GET", "/app/api/status", with("Host", "evil.example:53818"), 403},
		{"status localhost", "GET", "/app/api/status", with("Host", "localhost:53818"), 403},
		{"status other port", "GET", "/app/api/status", with("Host", "127.0.0.1:1"), 403},
		{"status POST", "POST", "/app/api/status", ok, 405},
		{"settings", "POST", "/app/api/settings", ok, 200},
		{"settings GET", "GET", "/app/api/settings", ok, 405},
		{"settings without token", "POST", "/app/api/settings", without(tokenHeader), 403},
		{"settings without Origin", "POST", "/app/api/settings", without("Origin"), 403},
		{"settings other Origin", "POST", "/app/api/settings", with("Origin", "http://evil.example"), 403},
		{"settings null Origin", "POST", "/app/api/settings", with("Origin", "null"), 403},
		{"settings https Origin", "POST", "/app/api/settings", with("Origin", "https://"+testAddr.String()), 403},
		{"settings cross-site", "POST", "/app/api/settings", with("Sec-Fetch-Site", "cross-site"), 403},
		{"settings user typed", "POST", "/app/api/settings", with("Sec-Fetch-Site", "none"), 403},
		{"settings no Sec-Fetch-Site", "POST", "/app/api/settings", without("Sec-Fetch-Site"), 200},
		{"settings form", "POST", "/app/api/settings", with("Content-Type", "application/x-www-form-urlencoded"), 415},
		{"settings text", "POST", "/app/api/settings", with("Content-Type", "text/plain"), 415},
		{"settings no type", "POST", "/app/api/settings", without("Content-Type"), 415},
		{"settings json charset", "POST", "/app/api/settings", with("Content-Type", "application/json; charset=utf-8"), 200},
		{"settings other host", "POST", "/app/api/settings", with("Host", "evil.example:53818"), 403},
		{"test", "POST", "/app/api/test", ok, 200},
		{"test without token", "POST", "/app/api/test", without(tokenHeader), 403},
		{"test other Origin", "POST", "/app/api/test", with("Origin", "http://evil.example"), 403},
		{"folder without a window", "POST", "/app/api/folder", ok, 404},
		{"unknown api", "GET", "/app/api/nothing", ok, 404},
		{"unknown api without token", "GET", "/app/api/nothing", nil, 403},
		{"page", "GET", "/app/", nil, 200},
		{"page HEAD", "HEAD", "/app/", nil, 200},
		{"page user typed", "GET", "/app/", []string{"Sec-Fetch-Site", "none"}, 200},
		{"page other host", "GET", "/app/", []string{"Host", "evil.example:53818"}, 403},
		{"page cross-site", "GET", "/app/", []string{"Sec-Fetch-Site", "cross-site"}, 403},
		{"page POST", "POST", "/app/", nil, 405},
		{"script", "GET", "/app/app.js", nil, 200},
		{"no index.html", "GET", "/app/index.html", nil, 404},
		{"no web", "GET", "/app/web/app.js", nil, 404},
	}
	for _, tt := range tests {
		w := e.serve(tt.method, tt.path, goodInput, tt.header...)
		if w.Code != tt.code {
			t.Errorf("%s: %s %s = %d %q, want %d", tt.name, tt.method, tt.path, w.Code, w.Body.String(), tt.code)
		}
		for k, v := range map[string]string{
			"Content-Security-Policy":      csp,
			"X-Frame-Options":              "DENY",
			"Cross-Origin-Resource-Policy": "same-origin",
			"Referrer-Policy":              "no-referrer",
			"X-Content-Type-Options":       "nosniff",
			"Cache-Control":                "no-store",
		} {
			if got := w.Header().Values(k); len(got) != 1 || got[0] != v {
				t.Errorf("%s: %s = %q, want %q", tt.name, k, got, v)
			}
		}
		for k := range w.Header() {
			if strings.HasPrefix(k, "Access-Control-") {
				t.Errorf("%s: has %s", tt.name, k)
			}
		}
	}
}

func TestRefusedPostChangesNothing(t *testing.T) {
	e := newAppEnv(t, "")
	w := e.serve("POST", "/app/api/settings", goodInput, tokenHeader, testToken, "Origin", "http://evil.example", "Content-Type", "application/json")
	if w.Code != 403 {
		t.Fatalf("code %d", w.Code)
	}
	if _, err := os.Stat(e.path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused save wrote the file: %v", err)
	}
}

func TestBodyLimit(t *testing.T) {
	e := newAppEnv(t, "")
	big := `{"address":"192.0.2.10","folder":` + quote(abs("/x")) + `,"interval":"5","excludes":["` + strings.Repeat("a", maxJSON) + `"]}`
	if w := e.post("/app/api/settings", big); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over the limit = %d", w.Code)
	}
	if w := e.post("/app/api/settings", `{"address":`); w.Code != http.StatusBadRequest {
		t.Errorf("a bad body = %d", w.Code)
	}
	if w := e.post("/app/api/settings", `{"interval":5}`); w.Code != http.StatusBadRequest {
		t.Errorf("a number for the interval = %d", w.Code)
	}
}

func TestStatic(t *testing.T) {
	e := newAppEnv(t, "")
	for path, typ := range map[string]string{
		"/app/":          "text/html; charset=utf-8",
		"/app/app.js":    "text/javascript; charset=utf-8",
		"/app/style.css": "text/css; charset=utf-8",
		"/app/icon.svg":  "image/svg+xml",
	} {
		w := e.serve("GET", path, "")
		if w.Code != 200 || w.Header().Get("Content-Type") != typ || w.Body.Len() == 0 {
			t.Errorf("%s = %d, %q, %d bytes", path, w.Code, w.Header().Get("Content-Type"), w.Body.Len())
		}
	}
	page := e.serve("GET", "/app/", "").Body.String()
	for _, bad := range []string{"<script>", "style=", "<style", "http://", "https://", "javascript:", "onclick", "onload", "onsubmit"} {
		if strings.Contains(page, bad) {
			t.Errorf("index.html holds %q", bad)
		}
	}
	if strings.Count(page, "<script") != 1 || !strings.Contains(page, `<script type="module" src="/app/app.js"></script>`) {
		t.Error("index.html must load app.js as its only script")
	}
	for _, f := range []string{"web/index.html", "web/app.js", "web/style.css"} {
		b, _ := assets.ReadFile(f)
		if strings.Contains(string(b), "innerHTML") {
			t.Errorf("%s uses innerHTML", f)
		}
	}
	mine, _ := assets.ReadFile("web/icon.svg")
	browse, err := os.ReadFile(filepath.Join("..", "browse", "web", "icon.svg"))
	if err != nil || string(mine) != string(browse) {
		t.Errorf("the app's icon differs from browse's (%v)", err)
	}
}

func TestStatus(t *testing.T) {
	e := newAppEnv(t, `{"version":1,"address":"192.0.2.10:445","folder":"/data/Seestar"}`)
	w := e.serve("GET", "/app/api/status", "", tokenHeader, testToken)
	got := decode[struct {
		Version         string   `json:"version"`
		SetUp           bool     `json:"set_up"`
		Settings        Settings `json:"settings"`
		SettingsFile    string   `json:"settings_file"`
		History         string   `json:"history"`
		DefaultExcludes []string `json:"default_excludes"`
	}](t, w)
	if got.Version != "v9.9.9" || !got.SetUp || got.Settings.Address != "192.0.2.10:445" || got.Settings.IntervalMinutes != 5 || len(got.DefaultExcludes) != 9 {
		t.Errorf("status = %+v", got)
	}
	// The history is the saved folder's state file, where sync keeps it.
	sum := sha256.Sum256([]byte(abs("/data/Seestar")))
	if want := filepath.Join(e.s.stateDir, "sync", hex.EncodeToString(sum[:])+".jsonl"); got.History != want {
		t.Errorf("history = %q, want %q", got.History, want)
	}
	if got.SettingsFile != e.path {
		t.Errorf("settings file = %q, want %q", got.SettingsFile, e.path)
	}
	for _, data := range []string{"", `{"version":1,"folder":"/data/Seestar"}`, `{"address":"a:445","folder":""}`} {
		e := newAppEnv(t, data)
		got := decode[struct {
			SetUp   bool   `json:"set_up"`
			History string `json:"history"`
		}](t, e.serve("GET", "/app/api/status", "", tokenHeader, testToken))
		if got.SetUp || got.History != "" {
			t.Errorf("%q: set up %v, history %q", data, got.SetUp, got.History)
		}
	}
}

func TestSaveFieldErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{"empty", `{}`, map[string]string{"address": addressError, "folder": folderError, "interval": intervalError}},
		{"bad patterns", `{"address":"192.0.2.10","folder":` + quote(abs("/x")) + `,"interval":"5","excludes":["Video","","[Pp]review"],"includes":["  ","a/"]}`, map[string]string{
			"excludes": `Line 3: [ and \ are not supported; use * and ?, and / between folders. Correct this pattern and save again.`,
			"includes": "Line 2: omit the trailing /; a pattern matches files and folders. Correct this pattern and save again.",
		}},
		{"interval 0", `{"address":"192.0.2.10","folder":` + quote(abs("/x")) + `,"interval":"0"}`, map[string]string{"interval": intervalError}},
		{"interval 1441", `{"address":"192.0.2.10","folder":` + quote(abs("/x")) + `,"interval":"1441"}`, map[string]string{"interval": intervalError}},
		{"interval 1.5", `{"address":"192.0.2.10","folder":` + quote(abs("/x")) + `,"interval":"1.5"}`, map[string]string{"interval": intervalError}},
		{"relative folder", `{"address":"192.0.2.10","folder":"Seestar","interval":"5"}`, map[string]string{"folder": folderError}},
		{"tilde folder", `{"address":"192.0.2.10","folder":"~/Seestar","interval":"5"}`, map[string]string{"folder": folderError}},
		{"port 0", `{"address":"192.0.2.10:0","folder":` + quote(abs("/x")) + `,"interval":"5"}`, map[string]string{"address": addressError}},
	}
	for _, tt := range tests {
		e := newAppEnv(t, "")
		w := e.post("/app/api/settings", tt.input)
		if w.Code != 400 {
			t.Errorf("%s: code %d %q", tt.name, w.Code, w.Body.String())
			continue
		}
		got := decode[fieldErrors](t, w)
		if fmt.Sprint(got.Fields) != fmt.Sprint(tt.want) {
			t.Errorf("%s: fields\n%v\nwant\n%v", tt.name, got.Fields, tt.want)
		}
		if _, err := os.Stat(e.path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: a field error wrote the file", tt.name)
		}
	}
}

func TestNormalizeAddress(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.10":          "192.0.2.10:445",
		" 192.0.2.10:4445 ":   "192.0.2.10:4445",
		"seestar.local":       "seestar.local:445",
		"seestar.local:445":   "seestar.local:445",
		"fe80::1":             "[fe80::1]:445",
		"[fe80::1]":           "[fe80::1]:445",
		"[fe80::1]:4445":      "[fe80::1]:4445",
		"":                    "",
		"  ":                  "",
		":445":                "",
		"192.0.2.10:":         "",
		"192.0.2.10:0":        "",
		"192.0.2.10:65536":    "",
		"192.0.2.10:x":        "",
		"http://192.0.2.10":   "",
		"192.0.2.10/share":    "",
		"a b":                 "",
		"fe80::1:zz::":        "",
		`\\192.0.2.10\share`:  "",
		"user@192.0.2.10:445": "",
	} {
		got, ok := normalizeAddress(in)
		if got != want || ok != (want != "") {
			t.Errorf("normalizeAddress(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestSaveWritesAndKeepsUnknown(t *testing.T) {
	e := newAppEnv(t, `{"version":1,"future":true}`)
	w := e.post("/app/api/settings", goodInput)
	if w.Code != 200 {
		t.Fatalf("code %d %q", w.Code, w.Body.String())
	}
	got := decode[struct {
		Settings      Settings `json:"settings"`
		BrowseChanged bool     `json:"browse_changed"`
	}](t, w)
	want := Settings{Version: 1, Address: "192.0.2.10:445", Folder: abs("/data/Seestar"), IntervalMinutes: 5,
		Excludes: []string{"Video", "/Preview"}, Includes: []string{}, SkipBuiltIn: true, StartCopying: false, OlderThanDays: 7}
	if fmt.Sprint(got.Settings) != fmt.Sprint(want) || !got.BrowseChanged {
		t.Errorf("got %+v, want %+v and a browse change", got, want)
	}
	f, err := loadSettings(e.path)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.settings) != fmt.Sprint(want) || string(f.raw["future"]) != "true" {
		t.Errorf("file holds %+v %s", f.settings, f.raw)
	}
}

func TestSaveFailure(t *testing.T) {
	e := newAppEnv(t, "")
	// A folder where the file should be: the rename fails.
	if err := os.MkdirAll(filepath.Join(e.path, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := e.post("/app/api/settings", goodInput)
	got := decode[struct {
		Error string `json:"error"`
	}](t, w)
	if w.Code != 500 || !strings.HasPrefix(got.Error, "Couldn't save settings: ") ||
		!strings.HasSuffix(got.Error, ". Your changes have not been saved. Try saving again.") ||
		strings.Contains(got.Error, e.path) {
		t.Errorf("save over a folder = %d %q", w.Code, got.Error)
	}
	if st := decode[struct {
		SetUp bool `json:"set_up"`
	}](t, e.serve("GET", "/app/api/status", "", tokenHeader, testToken)); st.SetUp {
		t.Error("a failed save finished Setup")
	}
	if w := e.serve("GET", "/", ""); w.Code != 404 {
		t.Errorf("a failed save started Browse: %d", w.Code)
	}
}

// smbStatus is a server's answer with an NTSTATUS, as go-smb2 returns it.
func smbStatus(code uint32) error { return &protocol.ResponseError{Code: code} }

func TestTestMessages(t *testing.T) {
	dirs := func(names ...string) []remote.Entry {
		var out []remote.Entry
		for _, n := range names {
			out = append(out, remote.Entry{Path: n, Kind: remote.Dir})
		}
		return append(out, remote.Entry{Path: "file.txt", Kind: remote.Regular})
	}
	tests := []struct {
		entries []remote.Entry
		err     error
		want    string
	}{
		{dirs(), nil, "Connected. The telescope's share has no folders."},
		{dirs("MyWorks"), nil, "Connected. The telescope's share has the folder MyWorks."},
		{dirs("MyWorks", "Autorun"), nil, "Connected. The telescope's share has the folders MyWorks and Autorun."},
		{dirs("MyWorks", "Autorun", "log"), nil, "Connected. The telescope's share has MyWorks, Autorun and 1 more folder."},
		{dirs("MyWorks", "Autorun", "log", "a", "b"), nil, "Connected. The telescope's share has MyWorks, Autorun and 3 more folders."},
		{nil, fmt.Errorf("connect to x: dial: %w", &net.DNSError{Err: "no such host", Name: "seestar.local", IsNotFound: true}), "Can't find seestar.local. Try the telescope's IP address."},
		{nil, fmt.Errorf("connect to x: dial: %w", &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}), "seestar.local answered but refused port 4445. Is this the telescope's address?"},
		{nil, fmt.Errorf("connect to x: %w", context.DeadlineExceeded), "Can't reach seestar.local:4445. Check that the telescope is on and on the same network as this computer, or that seestar-wormhole is running."},
		{nil, &net.DNSError{Err: "server misbehaving", Name: "seestar.local", IsTemporary: true}, "Can't reach seestar.local:4445. Check that the telescope is on and on the same network as this computer, or that seestar-wormhole is running."},
		// Mount's error, as remote and engine wrap it.
		{nil, fmt.Errorf("connect to x: mount \"EMMC Images\" on x: %w", &fs.PathError{Op: "mount", Path: `\\x\EMMC Images`, Err: smbStatus(0xC00000CC)}),
			`Connected, but this isn't a Seestar: it has no "EMMC Images" share.`},
		// Wrong credentials, or a share it may not use: not this case.
		{nil, fmt.Errorf("connect to x: dial x: %w", smbStatus(0xC000006D)), "Can't reach seestar.local:4445. Check that the telescope is on and on the same network as this computer, or that seestar-wormhole is running."},
		{nil, fmt.Errorf("connect to x: mount \"EMMC Images\" on x: %w", &fs.PathError{Op: "mount", Path: `\\x\EMMC Images`, Err: smbStatus(0xC0000022)}), "Can't reach seestar.local:4445. Check that the telescope is on and on the same network as this computer, or that seestar-wormhole is running."},
	}
	for _, tt := range tests {
		if got := testMessage("seestar.local:4445", tt.entries, tt.err); got != tt.want {
			t.Errorf("testMessage(%v, %v) = %q\nwant %q", tt.entries, tt.err, got, tt.want)
		}
	}
}

func TestTestAPI(t *testing.T) {
	e := newAppEnv(t, "")
	var tested []string
	e.s.test = func(ctx context.Context, endpoint string) ([]remote.Entry, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("Test has no deadline")
		}
		tested = append(tested, endpoint)
		if endpoint == "192.0.2.99:445" {
			return nil, errors.New("i/o timeout")
		}
		return []remote.Entry{{Path: "MyWorks", Kind: remote.Dir}}, nil
	}
	type answer struct {
		OK      bool   `json:"ok"`
		Address string `json:"address"`
		Message string `json:"message"`
	}
	if got := decode[answer](t, e.post("/app/api/test", `{"address":" 192.0.2.10 "}`)); !got.OK || got.Address != "192.0.2.10:445" ||
		got.Message != "Connected. The telescope's share has the folder MyWorks." {
		t.Errorf("test = %+v", got)
	}
	if got := decode[answer](t, e.post("/app/api/test", `{"address":"192.0.2.99"}`)); got.OK ||
		!strings.HasPrefix(got.Message, "Can't reach 192.0.2.99:445. ") {
		t.Errorf("test = %+v", got)
	}
	w := e.post("/app/api/test", `{"address":"http://x"}`)
	if got := decode[fieldErrors](t, w); w.Code != 400 || fmt.Sprint(got.Fields) != fmt.Sprint(map[string]string{"address": addressError}) {
		t.Errorf("a bad address = %d %v", w.Code, got)
	}
	// Test checks only the address: other fields in error don't stop it.
	if w := e.post("/app/api/test", `{"address":"192.0.2.10","interval":"0","folder":"x"}`); w.Code != 200 {
		t.Errorf("test with other fields in error = %d", w.Code)
	}
	if fmt.Sprint(tested) != "[192.0.2.10:445 192.0.2.99:445 192.0.2.10:445]" {
		t.Errorf("tested %v", tested)
	}
	if _, err := os.Stat(e.path); !errors.Is(err, fs.ErrNotExist) {
		t.Error("Test wrote the settings file")
	}
}

// listing is one browse /api/list answer's entries' names.
func listing(t *testing.T, e *appEnv) []string {
	t.Helper()
	w := e.serve("GET", "/api/list", "")
	if w.Code != 200 {
		t.Fatalf("browse list = %d %q", w.Code, w.Body.String())
	}
	got := decode[struct {
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
	}](t, w)
	var names []string
	for _, en := range got.Entries {
		names = append(names, en.Path)
	}
	return names
}

func TestBrowseSwap(t *testing.T) {
	e := newAppEnv(t, "")
	if w := e.serve("GET", "/", ""); w.Code != 404 || w.Header().Get("Content-Security-Policy") != csp {
		t.Errorf("browse before Setup = %d", w.Code)
	}
	if w := e.post("/app/api/settings", goodInput); w.Code != 200 {
		t.Fatal(w.Code)
	}
	page := e.serve("GET", "/", "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), `class="in-app"`) || page.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("browse after Setup = %d, %q", page.Code, page.Header())
	}
	if got := fmt.Sprint(listing(t, e)); got != "[at 192.0.2.10_445]" {
		t.Errorf("first share lists %s", got)
	}
	// Same address and folder: the pool stays.
	same := strings.Replace(goodInput, `"interval":"5"`, `"interval":"9"`, 1)
	if got := decode[struct {
		BrowseChanged bool `json:"browse_changed"`
	}](t, e.post("/app/api/settings", same)); got.BrowseChanged {
		t.Error("an interval change replaced browse")
	}
	listing(t, e)
	e.d.mu.Lock()
	first, n := e.d.sessions[0], len(e.d.sessions)
	e.d.mu.Unlock()
	if n != 1 {
		t.Errorf("%d dials before the address changed, want 1", n)
	}
	// A new address: a new pool, and the old one's session is closed.
	moved := strings.Replace(goodInput, `"192.0.2.10"`, `"192.0.2.20:4445"`, 1)
	if got := decode[struct {
		BrowseChanged bool `json:"browse_changed"`
	}](t, e.post("/app/api/settings", moved)); !got.BrowseChanged {
		t.Error("an address change kept browse")
	}
	if got := fmt.Sprint(listing(t, e)); got != "[at 192.0.2.20_4445]" {
		t.Errorf("second share lists %s", got)
	}
	if _, err := first.ReadDir(context.Background(), "."); err == nil {
		t.Error("the old pool's session is still open")
	}
	// A new folder also replaces it.
	refolder := strings.Replace(moved, quote(abs("/data/Seestar")), quote(abs("/data/Other")), 1)
	if got := decode[struct {
		BrowseChanged bool `json:"browse_changed"`
	}](t, e.post("/app/api/settings", refolder)); !got.BrowseChanged {
		t.Error("a folder change kept browse")
	}
}

func TestBrowseStatuses(t *testing.T) {
	// Browse's statuses come from the saved folder's state in the state
	// folder: with no state, every file is new.
	e := newAppEnv(t, `{"version":1,"address":"192.0.2.10:445","folder":"`+filepath.ToSlash(t.TempDir())+`"}`)
	w := e.serve("GET", "/api/list?path=at%20192.0.2.10_445", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"new"`) {
		t.Errorf("list = %d %s", w.Code, w.Body.String())
	}
}

func TestRunStartupError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.json")
	writeFile(t, path, "{")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	opened := false
	err := Run(context.Background(), Options{SettingsPath: path, StateDir: t.TempDir(), Version: "v", Log: log},
		func(context.Context, string, func()) error { opened = true; return nil })
	want := "Couldn't read settings from " + path + ": unexpected end of JSON input. Fix or remove the file, then reopen seestar-copy."
	if err == nil || err.Error() != want || opened {
		t.Errorf("Run = %v (opened %v), want %q", err, opened, want)
	}
}

func TestRunServes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.json")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var url string
	err := Run(ctx, Options{SettingsPath: path, StateDir: t.TempDir(), Version: "v", Log: log}, func(ctx context.Context, u string, _ func()) error {
		url = u
		base, tok, ok := strings.Cut(u, "/app/#")
		if !ok || !strings.HasPrefix(base, "http://127.0.0.1:") || len(tok) != 64 {
			return fmt.Errorf("url %q", u)
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/app/api/status", nil)
		req.Header.Set(tokenHeader, tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("status = %d", resp.StatusCode)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	base, _, _ := strings.Cut(url, "/app/#")
	if _, err := http.Get(base + "/app/"); err == nil {
		t.Error("the app still serves after Run returned")
	}
}
