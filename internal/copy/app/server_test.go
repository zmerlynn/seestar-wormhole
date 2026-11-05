package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
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

	"github.com/hirochachacha/go-smb2/v2/x/protocol"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var testAddr = netip.MustParseAddrPort("127.0.0.1:53818")

// dials records the sessions opened, by endpoint.
type dials struct {
	mu        sync.Mutex
	dir       string // if set, every session is this folder's
	endpoints []string
	sessions  []*remote.Fake
}

func (d *dials) dial(ctx context.Context, endpoint string) (remote.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dir != "" {
		return remote.NewFakeDir(d.dir)
	}
	// Each address has its own share: one folder named for it.
	f := remote.NewFake(fstest.MapFS{"at " + strings.ReplaceAll(endpoint, ":", "_") + "/x.txt": {Data: []byte("x")}})
	d.endpoints = append(d.endpoints, endpoint)
	d.sessions = append(d.sessions, f)
	return f, nil
}

// testEndpoint is where the app env's target is.
const testEndpoint = "192.0.2.10:445"

type appEnv struct {
	t *testing.T
	m *Mount
	s *server
	d *dials

	mu       sync.Mutex
	endpoint string     // the target's
	saved    []Settings // each Save, in order
	saveErr  error      // what Save fails with
}

// newAppEnv is a Mount's server with saved settings, or nil for none,
// whose target is testEndpoint and whose Save records what it is given.
func newAppEnv(t *testing.T, saved *Settings) *appEnv {
	t.Helper()
	e := &appEnv{t: t, d: &dials{}, endpoint: testEndpoint}
	m := NewMount(context.Background(), MountOptions{
		Addr: testAddr, StateDir: t.TempDir(), Version: "v9.9.9",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Target: func() (Target, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			return Target{Endpoint: e.endpoint}, nil
		},
		Dial: e.d.dial,
		Test: func(ctx context.Context, endpoint string) ([]remote.Entry, error) {
			return []remote.Entry{{Path: "MyWorks", Kind: remote.Dir}}, nil
		},
		Settings: saved,
		Save: func(set Settings) error {
			e.mu.Lock()
			defer e.mu.Unlock()
			if e.saveErr != nil {
				return e.saveErr
			}
			e.saved = append(e.saved, set)
			return nil
		},
	})
	e.m, e.s = m, m.s
	t.Cleanup(m.Quit)
	return e
}

// move changes where the telescope is, as a reconnect does.
func (e *appEnv) move(endpoint string) {
	e.t.Helper()
	if err := e.m.Change(func() bool { return true }, func() error {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.endpoint = endpoint
		return nil
	}); err != nil {
		e.t.Fatal(err)
	}
}

// savedAt is the default settings with folder, as saved before.
func savedAt(folder string) *Settings {
	set := DefaultSettings()
	set.Folder = folder
	return &set
}

// saves is what Save was given, in order.
func (e *appEnv) saves() []Settings {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Settings(nil), e.saved...)
}

// serve sends one request; the mounting server has made its checks.
// headers are name and value pairs; "Host" sets the Host, which is
// otherwise the listener's address.
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
	return e.serve("POST", path, body, "Content-Type", "application/json")
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

var goodInput = `{"folder":` + quote(abs("/data/Seestar")) + `,"interval":"5","excludes":["Video","","/Preview"],"includes":[],"skip_built_in":true,"start_copying":false}`

func TestBodyLimit(t *testing.T) {
	e := newAppEnv(t, nil)
	big := `{"folder":` + quote(abs("/x")) + `,"interval":"5","excludes":["` + strings.Repeat("a", maxJSON) + `"]}`
	if w := e.post("/copy/api/settings", big); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body over the limit = %d", w.Code)
	}
	if w := e.post("/copy/api/settings", `{"folder":`); w.Code != http.StatusBadRequest {
		t.Errorf("a bad body = %d", w.Code)
	}
	if w := e.post("/copy/api/settings", `{"interval":5}`); w.Code != http.StatusBadRequest {
		t.Errorf("a number for the interval = %d", w.Code)
	}
}

func TestStatus(t *testing.T) {
	e := newAppEnv(t, savedAt("/data/Seestar"))
	w := e.serve("GET", "/copy/api/status", "")
	got := decode[struct {
		Version         string         `json:"version"`
		SetUp           bool           `json:"set_up"`
		Settings        map[string]any `json:"settings"`
		History         string         `json:"history"`
		DefaultExcludes []string       `json:"default_excludes"`
	}](t, w)
	if got.Version != "v9.9.9" || !got.SetUp || got.Settings["interval_minutes"] != 5.0 || len(got.DefaultExcludes) != 9 {
		t.Errorf("status = %+v", got)
	}
	// The address is the connection's, not a setting.
	if _, ok := got.Settings["address"]; ok {
		t.Errorf("status settings hold an address: %v", got.Settings)
	}
	// The history is the saved folder's state file, where sync keeps it.
	sum := sha256.Sum256([]byte(abs("/data/Seestar")))
	if want := filepath.Join(e.s.stateDir, "sync", hex.EncodeToString(sum[:])+".jsonl"); got.History != want {
		t.Errorf("history = %q, want %q", got.History, want)
	}
	for name, set := range map[string]*Settings{"none saved": nil, "no folder": savedAt("")} {
		e := newAppEnv(t, set)
		got := decode[struct {
			SetUp   bool   `json:"set_up"`
			History string `json:"history"`
		}](t, e.serve("GET", "/copy/api/status", ""))
		if got.SetUp || got.History != "" {
			t.Errorf("%s: set up %v, history %q", name, got.SetUp, got.History)
		}
	}
}

func TestSaveFieldErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{"empty", `{}`, map[string]string{"folder": folderError, "interval": intervalError}},
		{"bad patterns", `{"folder":` + quote(abs("/x")) + `,"interval":"5","excludes":["Video","","[Pp]review"],"includes":["  ","a/"]}`, map[string]string{
			"excludes": `Line 3: [ and \ are not supported; use * and ?, and / between folders. Correct this pattern and save again.`,
			"includes": "Line 2: omit the trailing /; a pattern matches files and folders. Correct this pattern and save again.",
		}},
		{"interval 0", `{"folder":` + quote(abs("/x")) + `,"interval":"0"}`, map[string]string{"interval": intervalError}},
		{"interval 1441", `{"folder":` + quote(abs("/x")) + `,"interval":"1441"}`, map[string]string{"interval": intervalError}},
		{"interval 1.5", `{"folder":` + quote(abs("/x")) + `,"interval":"1.5"}`, map[string]string{"interval": intervalError}},
		{"relative folder", `{"folder":"Seestar","interval":"5"}`, map[string]string{"folder": folderError}},
		{"tilde folder", `{"folder":"~/Seestar","interval":"5"}`, map[string]string{"folder": folderError}},
		// The address is the connection's; the form's is ignored.
		{"address", `{"address":"192.0.2.10:0","interval":"5"}`, map[string]string{"folder": folderError}},
	}
	for _, tt := range tests {
		e := newAppEnv(t, nil)
		w := e.post("/copy/api/settings", tt.input)
		if w.Code != 400 {
			t.Errorf("%s: code %d %q", tt.name, w.Code, w.Body.String())
			continue
		}
		got := decode[fieldErrors](t, w)
		if fmt.Sprint(got.Fields) != fmt.Sprint(tt.want) {
			t.Errorf("%s: fields\n%v\nwant\n%v", tt.name, got.Fields, tt.want)
		}
		if len(e.saves()) != 0 {
			t.Errorf("%s: a field error saved", tt.name)
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

func TestSaveCallsSave(t *testing.T) {
	e := newAppEnv(t, nil)
	w := e.post("/copy/api/settings", `{"address":"192.0.2.99",`+goodInput[1:])
	if w.Code != 200 {
		t.Fatalf("code %d %q", w.Code, w.Body.String())
	}
	got := decode[struct {
		Settings      Settings `json:"settings"`
		BrowseChanged bool     `json:"browse_changed"`
	}](t, w)
	want := Settings{Version: 1, Folder: abs("/data/Seestar"), IntervalMinutes: 5,
		Excludes: []string{"Video", "/Preview"}, Includes: []string{}, SkipBuiltIn: true, StartCopying: false, OlderThanDays: 7}
	if fmt.Sprint(got.Settings) != fmt.Sprint(want) || !got.BrowseChanged {
		t.Errorf("got %+v, want %+v and a browse change", got, want)
	}
	if saves := e.saves(); len(saves) != 1 || fmt.Sprint(saves[0]) != fmt.Sprint(want) {
		t.Errorf("saved %+v, want %+v", saves, want)
	}
}

func TestSaveFailure(t *testing.T) {
	e := newAppEnv(t, nil)
	e.saveErr = &fs.PathError{Op: "open", Path: abs("/private/copy.json"), Err: fs.ErrPermission}
	w := e.post("/copy/api/settings", goodInput)
	got := decode[struct {
		Error string `json:"error"`
	}](t, w)
	if w.Code != 500 || got.Error != "Couldn't save settings: permission denied. Your changes have not been saved. Try saving again." {
		t.Errorf("a failed save = %d %q", w.Code, got.Error)
	}
	if st := decode[struct {
		SetUp bool `json:"set_up"`
	}](t, e.serve("GET", "/copy/api/status", "")); st.SetUp {
		t.Error("a failed save finished Setup")
	}
	if w := e.serve("GET", "/browse/", ""); w.Code != 404 {
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
	e := newAppEnv(t, nil)
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
	if got := decode[answer](t, e.post("/copy/api/test", `{"address":" 192.0.2.10 "}`)); !got.OK || got.Address != "192.0.2.10:445" ||
		got.Message != "Connected. The telescope's share has the folder MyWorks." {
		t.Errorf("test = %+v", got)
	}
	if got := decode[answer](t, e.post("/copy/api/test", `{"address":"192.0.2.99"}`)); got.OK ||
		!strings.HasPrefix(got.Message, "Can't reach 192.0.2.99:445. ") {
		t.Errorf("test = %+v", got)
	}
	w := e.post("/copy/api/test", `{"address":"http://x"}`)
	if got := decode[fieldErrors](t, w); w.Code != 400 || fmt.Sprint(got.Fields) != fmt.Sprint(map[string]string{"address": addressError}) {
		t.Errorf("a bad address = %d %v", w.Code, got)
	}
	// Test checks only the address: other fields in error don't stop it.
	if w := e.post("/copy/api/test", `{"address":"192.0.2.10","interval":"0","folder":"x"}`); w.Code != 200 {
		t.Errorf("test with other fields in error = %d", w.Code)
	}
	if fmt.Sprint(tested) != "[192.0.2.10:445 192.0.2.99:445 192.0.2.10:445]" {
		t.Errorf("tested %v", tested)
	}
	if len(e.saves()) != 0 {
		t.Error("Test saved the settings")
	}
}

// listing is one browse /api/list answer's entries' names.
func listing(t *testing.T, e *appEnv) []string {
	t.Helper()
	w := e.serve("GET", "/browse/api/list", "")
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
	e := newAppEnv(t, nil)
	if w := e.serve("GET", "/browse/", ""); w.Code != 404 {
		t.Errorf("browse before Setup = %d", w.Code)
	}
	if w := e.post("/copy/api/settings", goodInput); w.Code != 200 {
		t.Fatal(w.Code)
	}
	page := e.serve("GET", "/browse/", "")
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
	}](t, e.post("/copy/api/settings", same)); got.BrowseChanged {
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
	e.move("192.0.2.20:4445")
	if got := fmt.Sprint(listing(t, e)); got != "[at 192.0.2.20_4445]" {
		t.Errorf("second share lists %s", got)
	}
	if _, err := first.ReadDir(context.Background(), "."); err == nil {
		t.Error("the old pool's session is still open")
	}
	// A new folder also replaces it.
	refolder := strings.Replace(goodInput, quote(abs("/data/Seestar")), quote(abs("/data/Other")), 1)
	if got := decode[struct {
		BrowseChanged bool `json:"browse_changed"`
	}](t, e.post("/copy/api/settings", refolder)); !got.BrowseChanged {
		t.Error("a folder change kept browse")
	}
}

func TestBrowseStatuses(t *testing.T) {
	// Browse's statuses come from the saved folder's state in the state
	// folder: with no state, every file is new.
	e := newAppEnv(t, savedAt(t.TempDir()))
	w := e.serve("GET", "/browse/api/list?path=at%20192.0.2.10_445", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"new"`) {
		t.Errorf("list = %d %s", w.Code, w.Body.String())
	}
}

func TestBrowseLeftOut(t *testing.T) {
	// Browse marks what the saved selection leaves out, and a save that
	// changes the selection replaces browse.
	e := newAppEnv(t, nil)
	list := func() string {
		w := e.serve("GET", "/browse/api/list?path=at%20192.0.2.10_445", "")
		if w.Code != 200 {
			t.Fatalf("list = %d %s", w.Code, w.Body)
		}
		return w.Body.String()
	}
	if w := e.post("/copy/api/settings", goodInput); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if got := list(); !strings.Contains(got, `"status":"new"`) {
		t.Errorf("before Leave out: %s", got)
	}
	// Each save changes one setting from goodInput.
	save := func(input, what string) {
		w := e.post("/copy/api/settings", input)
		if got := decode[struct {
			BrowseChanged bool `json:"browse_changed"`
		}](t, w); w.Code != 200 || !got.BrowseChanged {
			t.Errorf("saving %s kept browse: %d %s", what, w.Code, w.Body)
		}
		if got := list(); !strings.Contains(got, `"status":"left out"`) {
			t.Errorf("with %s: %s", what, got)
		}
	}
	save(strings.Replace(goodInput, `"excludes":["Video","","/Preview"]`, `"excludes":["X.TXT"]`, 1), "Leave out")
	if w := e.post("/copy/api/settings", goodInput); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if got := list(); !strings.Contains(got, `"status":"new"`) {
		t.Errorf("before Only copy: %s", got)
	}
	save(strings.Replace(goodInput, `"includes":[]`, `"includes":["*.fit"]`, 1), "Only copy")
}

// The page reads these bytes; they must stay as they were: zero and false
// fields left out, map members sorted.
func TestRespondShapes(t *testing.T) {
	fields := map[string]string{}
	want := `{"fields":{`
	for i := range 20 {
		k := fmt.Sprintf("f%02d", i)
		fields[k] = "x"
		if i > 0 {
			want += ","
		}
		want += `"` + k + `":"x"`
	}
	want += "}}"
	for _, tc := range []struct {
		name string
		v    any
		want string
	}{
		{"zero and false", freeView{State: "start"}, `{"state":"start"}`},
		{"sorted map", fieldErrors{fields}, want},
	} {
		w := httptest.NewRecorder()
		respond(w, 200, tc.v)
		if got := w.Body.String(); got != tc.want {
			t.Errorf("%s: respond wrote\n%s\nwant\n%s", tc.name, got, tc.want)
		}
	}
}

// The API reads a request strictly: a repeated name, a byte that is not
// UTF-8 or a name in the wrong case is not the page's, and is refused or
// ignored, where encoding/json took the last value, the bytes and any case.
func TestAPIReadsStrictly(t *testing.T) {
	e := newAppEnv(t, nil)
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"action":"start"}`, http.StatusConflict}, // no settings yet
		{`{"action":"jump","action":"start"}`, http.StatusBadRequest},
		{`{"action":"start","folder":"` + "\xff" + `"}`, http.StatusBadRequest},
		{`{"Action":"start"}`, http.StatusBadRequest}, // no action
	} {
		if w := e.post("/copy/api/copy", tc.body); w.Code != tc.code {
			t.Errorf("%q = %d, want %d", tc.body, w.Code, tc.code)
		}
	}
}

func TestRespondStrings(t *testing.T) {
	w := httptest.NewRecorder()
	respond(w, 200, struct {
		Slice []string          `json:"slice"`
		Map   map[string]string `json:"map"`
		Text  string            `json:"text"`
	}{Text: "<>&\u2028\u2029\xff"})
	want := `{"slice":null,"map":null,"text":"\u003c\u003e\u0026\u2028\u2029` + "\ufffd" + `"}`
	if got := w.Body.String(); got != want {
		t.Errorf("respond wrote\n%s\nwant\n%s", got, want)
	}
}
