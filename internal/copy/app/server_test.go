package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/localhttp"
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
	"sync/atomic"
	"syscall"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"

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
	lost     error      // what finding the target fails with
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
			return Target{Endpoint: e.endpoint}, e.lost
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

var goodInput = `{"folder":` + quote(abs("/data/Seestar")) + `,"interval":"5","files_at_once":" 2 ","excludes":["Video","","/Preview"],"includes":[],"skip_built_in":true,"start_copying":false}`

func TestBodyLimit(t *testing.T) {
	e := newAppEnv(t, nil)
	big := `{"folder":` + quote(abs("/x")) + `,"interval":"5","files_at_once":"4","excludes":["` + strings.Repeat("a", localhttp.MaxJSON) + `"]}`
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
		{"empty", `{}`, map[string]string{"files_at_once": filesError, "folder": folderError, "interval": intervalError}},
		{"bad patterns", `{"folder":` + quote(abs("/x")) + `,"interval":"5","files_at_once":"4","excludes":["Video","","[Pp]review"],"includes":["  ","a/"]}`, map[string]string{
			"excludes": `Line 3: [ and \ are not supported; use * and ?, and / between folders. Correct this pattern and save again.`,
			"includes": "Line 2: omit the trailing /; a pattern matches files and folders. Correct this pattern and save again.",
		}},
		{"interval 0", `{"folder":` + quote(abs("/x")) + `,"interval":"0","files_at_once":"4"}`, map[string]string{"interval": intervalError}},
		{"interval 1441", `{"folder":` + quote(abs("/x")) + `,"interval":"1441","files_at_once":"4"}`, map[string]string{"interval": intervalError}},
		{"interval 1.5", `{"folder":` + quote(abs("/x")) + `,"interval":"1.5","files_at_once":"4"}`, map[string]string{"interval": intervalError}},
		{"files at once 0", `{"folder":` + quote(abs("/x")) + `,"interval":"5","files_at_once":"0"}`, map[string]string{"files_at_once": filesError}},
		{"files at once 5", `{"folder":` + quote(abs("/x")) + `,"interval":"5","files_at_once":"5"}`, map[string]string{"files_at_once": filesError}},
		{"files at once 2.5", `{"folder":` + quote(abs("/x")) + `,"interval":"5","files_at_once":"2.5"}`, map[string]string{"files_at_once": filesError}},
		{"relative folder", `{"folder":"Seestar","interval":"5","files_at_once":"4"}`, map[string]string{"folder": folderError}},
		{"tilde folder", `{"folder":"~/Seestar","interval":"5","files_at_once":"4"}`, map[string]string{"folder": folderError}},
		// The address is the connection's; the form's is ignored.
		{"address", `{"address":"192.0.2.10:0","interval":"5","files_at_once":"4"}`, map[string]string{"folder": folderError}},
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
		Excludes: []string{"Video", "/Preview"}, Includes: []string{}, SkipBuiltIn: true, StartCopying: false, FilesAtOnce: 2, OlderThanDays: 7}
	if fmt.Sprint(got.Settings) != fmt.Sprint(want) || !got.BrowseChanged {
		t.Errorf("got %+v, want %+v and a browse change", got, want)
	}
	if saves := e.saves(); len(saves) != 1 || fmt.Sprint(saves[0]) != fmt.Sprint(want) {
		t.Errorf("saved %+v, want %+v", saves, want)
	}
	// Copy once is saved as posted.
	if w := e.post("/copy/api/settings", `{"once":true,`+goodInput[1:]); w.Code != 200 {
		t.Fatalf("code %d %q", w.Code, w.Body.String())
	}
	if saves := e.saves(); len(saves) != 2 || !saves[1].Once {
		t.Errorf("saved %+v, want copy once", saves)
	}
	// Copy once doesn't use the interval, so one that isn't valid doesn't
	// block the save: the default is kept for a later return to repeating.
	if w := e.post("/copy/api/settings", `{"once":true,`+strings.Replace(goodInput[1:], `"interval":"5"`, `"interval":""`, 1)); w.Code != 200 {
		t.Fatalf("copy once without an interval: code %d %q", w.Code, w.Body.String())
	}
	if saves := e.saves(); len(saves) != 3 || !saves[2].Once || saves[2].IntervalMinutes != 5 {
		t.Errorf("saved %+v, want copy once every 5 minutes", saves)
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
		{nil, fmt.Errorf("connect to x: %w", context.DeadlineExceeded), "Can't reach seestar.local:4445. Check that the telescope is on and on this computer's network."},
		{nil, &net.DNSError{Err: "server misbehaving", Name: "seestar.local", IsTemporary: true}, "Can't reach seestar.local:4445. Check that the telescope is on and on this computer's network."},
		// Mount's error, as remote and engine wrap it.
		{nil, fmt.Errorf("connect to x: mount \"EMMC Images\" on x: %w", &fs.PathError{Op: "mount", Path: `\\x\EMMC Images`, Err: smbStatus(0xC00000CC)}),
			`Connected, but this isn't a Seestar: it has no "EMMC Images" share.`},
		// Wrong credentials, or a share it may not use: not this case.
		{nil, fmt.Errorf("connect to x: dial x: %w", smbStatus(0xC000006D)), "Can't reach seestar.local:4445. Check that the telescope is on and on this computer's network."},
		{nil, fmt.Errorf("connect to x: mount \"EMMC Images\" on x: %w", &fs.PathError{Op: "mount", Path: `\\x\EMMC Images`, Err: smbStatus(0xC0000022)}), "Can't reach seestar.local:4445. Check that the telescope is on and on this computer's network."},
	}
	for _, tt := range tests {
		if got := testMessage("linux", "seestar.local:4445", tt.entries, tt.err); got != tt.want {
			t.Errorf("testMessage(%v, %v) = %q\nwant %q", tt.entries, tt.err, got, tt.want)
		}
	}
}

// setHostOS has the page worded for goos until t ends.
func setHostOS(t *testing.T, goos string) {
	old := hostOS
	hostOS = goos
	t.Cleanup(func() { hostOS = old })
}

const lanHint = " On macOS, if Local Network access is blocked, enable seestar-wormhole in System Settings > Privacy & Security > Local Network, then Test again. If a permission request is open, allow access first."

// connectFailed is a TCP connect that failed with errno, as net's dial
// returns it, wrapped as Dial wraps it.
func connectFailed(errno syscall.Errno) error {
	return fmt.Errorf("dial 192.0.2.10:445: %w", &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno)})
}

// noRoute is a connect with no route to the host.
var noRoute = connectFailed(syscall.EHOSTUNREACH)

// On macOS, a TCP connect that nothing answered points at Local Network
// access: a timeout, a deadline that passed while connecting, no route to
// the host or network, or a connect the system refused to send. A
// refusal, a name not found, a DNS server's answer and any SMB answer
// don't, and nor does any failure elsewhere.
func TestLocalNetworkHint(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	// The context ends before net dials, so nothing is sent.
	_, deadline := (&net.Dialer{}).DialContext(expired, "tcp", "192.0.2.10:445")
	if !errors.Is(deadline, context.DeadlineExceeded) {
		t.Fatalf("dial with an expired context = %v", deadline)
	}
	deadline = fmt.Errorf("dial 192.0.2.10:445: %w", deadline)
	dnsTimeout := fmt.Errorf("dial seestar.local:445: %w", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "i/o timeout", Name: "seestar.local", IsTimeout: true}})
	signing := &protocol.InvalidResponseError{Message: "session setup response failed signature verification"}
	// Something answered the connect, then went quiet.
	readTimeout := fmt.Errorf("dial 192.0.2.10:445: %w", &protocol.TransportError{Err: &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ETIMEDOUT)}})
	tests := []struct {
		name string
		goos string
		err  error
		want string
	}{
		{"deadline while connecting", "darwin", deadline, lanHint},
		{"connect timed out", "darwin", connectFailed(syscall.ETIMEDOUT), lanHint},
		{"DNS timed out", "darwin", dnsTimeout, lanHint},
		{"no route to host", "darwin", noRoute, lanHint},
		{"network unreachable", "darwin", connectFailed(syscall.ENETUNREACH), lanHint},
		{"on-link host down", "darwin", connectFailed(syscall.EHOSTDOWN), lanHint},
		{"connect not permitted", "darwin", connectFailed(syscall.EPERM), lanHint},
		{"Linux", "linux", deadline, ""},
		{"Windows", "windows", noRoute, ""},
		{"refused", "darwin", connectFailed(syscall.ECONNREFUSED), ""},
		{"name not found", "darwin", fmt.Errorf("dial seestar.local:445: %w", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "seestar.local", IsNotFound: true}}), ""},
		{"DNS server's answer", "darwin", fmt.Errorf("dial seestar.local:445: %w", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "server misbehaving", Name: "seestar.local", IsTemporary: true}}), ""},
		{"deadline after connecting", "darwin", fmt.Errorf("mount \"EMMC Images\" on 192.0.2.10:445: %w", context.DeadlineExceeded), ""},
		{"read timed out after connecting", "darwin", readTimeout, ""},
		{"logon failure", "darwin", fmt.Errorf("dial 192.0.2.10:445: %w", smbStatus(0xC000006D)), ""},
		{"no share", "darwin", fmt.Errorf("mount \"EMMC Images\" on 192.0.2.10:445: %w", &fs.PathError{Op: "mount", Path: `\\192.0.2.10\EMMC Images`, Err: smbStatus(0xC00000CC)}), ""},
		{"signature", "darwin", fmt.Errorf("dial 192.0.2.10:445: %w", signing), ""},
		{"guest signing", "darwin", fmt.Errorf("dial 192.0.2.10:445: %w", &protocol.InvalidResponseError{Message: "guest account doesn't support signing"}), ""},
		{"authentication", "darwin", fmt.Errorf("dial 192.0.2.10:445: %w", errors.New("auth: invalid credential")), ""},
	}
	for _, tt := range tests {
		if got := LocalNetworkHint(tt.goos, tt.err); got != tt.want {
			t.Errorf("%s: LocalNetworkHint(%s, %v) = %q, want %q", tt.name, tt.goos, tt.err, got, tt.want)
		}
	}
	want := "Can't reach seestar.local:4445. Check that the telescope is on and on this computer's network." + lanHint
	if got := testMessage("darwin", "seestar.local:4445", nil, deadline); got != want {
		t.Errorf("Test on macOS = %q\nwant %q", got, want)
	}
	want = "seestar.local answered but refused port 4445. Is this the telescope's address?"
	if got := testMessage("darwin", "seestar.local:4445", nil, fmt.Errorf("dial: %w", syscall.ECONNREFUSED)); got != want {
		t.Errorf("a refusal on macOS = %q, want %q", got, want)
	}
}

// setRetry makes localNetworkRetry a new one, as when the app starts, that
// runs on goos at its own interval and limit, with a clock that moves only
// while it waits. It returns how long it waited.
func setRetry(t *testing.T, goos string) *atomic.Int64 {
	var waited atomic.Int64
	start := time.Unix(1, 0)
	old := localNetworkRetry
	localNetworkRetry = &lanRetry{
		goos: goos, every: old.every, limit: old.limit,
		now: func() time.Time { return start.Add(time.Duration(waited.Load())) },
		after: func(d time.Duration) <-chan time.Time {
			waited.Add(int64(d))
			c := make(chan time.Time, 1)
			c <- start
			return c
		},
	}
	t.Cleanup(func() { localNetworkRetry = old })
	return &waited
}

// newRetry is a new localNetworkRetry on macOS with the real clock, for a
// synctest bubble.
func newRetry() *lanRetry {
	return &lanRetry{goos: "darwin", every: localNetworkRetry.every, limit: localNetworkRetry.limit, now: time.Now, after: time.After}
}

// On macOS, a dial the system failed at once is tried again every second,
// as when the Local Network prompt is open, until it goes through or 5 s
// have passed. A timeout, a refusal, a DNS failure or any SMB answer
// returns at once, as does every failure elsewhere.
func TestRetryLAN(t *testing.T) {
	if localNetworkRetry.every != time.Second || localNetworkRetry.limit != 5*time.Second {
		t.Errorf("retry every %v for %v, want every 1s for 5s", localNetworkRetry.every, localNetworkRetry.limit)
	}
	smb := fmt.Errorf("dial 192.0.2.10:445: %w", smbStatus(0xC000006D))
	dns := fmt.Errorf("dial seestar.local:445: %w", &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "seestar.local", IsNotFound: true}})
	tests := []struct {
		name  string
		goos  string
		err   error
		tries int
	}{
		{"no route to host", "darwin", noRoute, 3},
		{"network unreachable", "darwin", connectFailed(syscall.ENETUNREACH), 3},
		{"on-link host down", "darwin", connectFailed(syscall.EHOSTDOWN), 3},
		{"connect not permitted", "darwin", connectFailed(syscall.EPERM), 3},
		{"connect timed out", "darwin", connectFailed(syscall.ETIMEDOUT), 1},
		{"deadline", "darwin", fmt.Errorf("dial 192.0.2.10:445: %w", &net.OpError{Op: "dial", Net: "tcp", Err: context.DeadlineExceeded}), 1},
		{"refused", "darwin", connectFailed(syscall.ECONNREFUSED), 1},
		{"name not found", "darwin", dns, 1},
		{"logon failure", "darwin", smb, 1},
		{"Linux", "linux", noRoute, 1},
		{"Windows", "windows", noRoute, 1},
	}
	for _, tt := range tests {
		waited := setRetry(t, tt.goos)
		tries := 0
		// The third try goes through.
		got, err := retryLAN(t.Context(), localNetworkRetry, func(context.Context) (int, error) {
			if tries++; tries < 3 {
				return 0, tt.err
			}
			return 7, nil
		})
		if tries != tt.tries || time.Duration(waited.Load()) != time.Duration(tt.tries-1)*time.Second {
			t.Errorf("%s on %s: %d tries over %v, want %d", tt.name, tt.goos, tries, time.Duration(waited.Load()), tt.tries)
		}
		if tt.tries == 3 && (got != 7 || err != nil) {
			t.Errorf("%s on %s: %v, %v after the try that went through", tt.name, tt.goos, got, err)
		}
		if tt.tries == 1 && err != tt.err {
			t.Errorf("%s on %s: %v, want %v", tt.name, tt.goos, err, tt.err)
		}
		// Only the first dial waits: one after it fails at once.
		tries, before := 0, waited.Load()
		if _, err := retryLAN(t.Context(), localNetworkRetry, func(context.Context) (int, error) { tries++; return 0, noRoute }); err != noRoute || tries != 1 || waited.Load() != before {
			t.Errorf("%s on %s, then a blocked connect: %v after %d tries over %v", tt.name, tt.goos, err, tries, time.Duration(waited.Load()-before))
		}
	}

	// It gives up after 5 s with the last try's error, and later dials
	// don't wait.
	waited := setRetry(t, "darwin")
	for _, want := range []int{5, 1} {
		tries := 0
		if _, err := retryLAN(t.Context(), localNetworkRetry, func(context.Context) (int, error) { tries++; return 0, noRoute }); err != noRoute ||
			tries != want || time.Duration(waited.Load()) != 4*time.Second {
			t.Errorf("a blocked connect: %v after %d tries over %v, want %d tries over 4s", err, tries, time.Duration(waited.Load()), want)
		}
	}
	// The caller's context ends it too, with the dial's error. That dial
	// was the first, so the next one doesn't wait either.
	waited = setRetry(t, "darwin")
	ctx, cancel := context.WithCancel(t.Context())
	tries := 0
	if _, err := retryLAN(ctx, localNetworkRetry, func(context.Context) (int, error) {
		if tries++; tries == 2 {
			cancel()
		}
		return 0, noRoute
	}); err != noRoute || tries != 2 {
		t.Errorf("a canceled retry: %v after %d tries", err, tries)
	}
	tries, before := 0, waited.Load()
	if _, err := retryLAN(t.Context(), localNetworkRetry, func(context.Context) (int, error) { tries++; return 0, noRoute }); err != noRoute ||
		tries != 1 || waited.Load() != before {
		t.Errorf("after a canceled retry: %v after %d tries over %v", err, tries, time.Duration(waited.Load()-before))
	}
}

// The limit counts the time spent dialing, not just waiting: a try again
// gets only what is left, and the failure before it is returned.
func TestRetryLANLimitCountsDials(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRetry()
		start := time.Now()
		tries := 0
		var cut context.Context
		_, err := retryLAN(t.Context(), r, func(ctx context.Context) (int, error) {
			tries++
			select {
			case <-time.After(3 * time.Second):
				return 0, noRoute
			case <-ctx.Done():
				cut = ctx
				return 0, ctx.Err()
			}
		})
		if elapsed := time.Since(start); elapsed != r.limit || tries != 2 || err != noRoute || cut == nil {
			t.Errorf("%v after %d tries over %v, cut %v", err, tries, elapsed, cut != nil)
		}
	})
}

// A try again that goes through keeps the caller's context: the limit
// doesn't end the session it dialed.
func TestRetryLANKeepsSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tries := 0
		sess, err := retryLAN(t.Context(), newRetry(), func(ctx context.Context) (context.Context, error) {
			if tries++; tries == 1 {
				return nil, noRoute
			}
			return ctx, nil
		})
		time.Sleep(time.Minute)
		if err != nil || tries != 2 || sess.Err() != nil {
			t.Errorf("%v after %d tries; session context %v", err, tries, sess.Err())
		}
	})
}

// A timer that fires late doesn't start a dial past the limit.
func TestRetryLANLateTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRetry()
		r.after = func(d time.Duration) <-chan time.Time { return time.After(d + 10*time.Second) }
		tries := 0
		for range 2 {
			if _, err := retryLAN(t.Context(), r, func(context.Context) (int, error) { tries++; return 0, noRoute }); err != noRoute {
				t.Errorf("err %v", err)
			}
		}
		if tries != 2 {
			t.Errorf("%d tries, want one for each dial", tries)
		}
	})
}

// Canceling the caller while the retry waits returns at once, without
// another try.
func TestRetryLANCanceledWhileWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		tries := 0
		start := time.Now()
		go func() {
			_, err := retryLAN(ctx, newRetry(), func(context.Context) (int, error) { tries++; return 0, noRoute })
			done <- err
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if err != noRoute || tries != 1 || time.Since(start) != 0 {
				t.Errorf("%v after %d tries over %v", err, tries, time.Since(start))
			}
		default:
			t.Error("still waiting after the caller was canceled")
			<-done
		}
	})
}

// A first dial canceled while it waits has still used the retries: the
// next dial tries once.
func TestRetryLANCanceledFirstIsSpent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRetry()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			retryLAN(ctx, r, func(context.Context) (int, error) { return 0, noRoute })
		}()
		synctest.Wait()
		cancel()
		<-done
		start, tries := time.Now(), 0
		if _, err := retryLAN(t.Context(), r, func(context.Context) (int, error) { tries++; return 0, noRoute }); err != noRoute || tries != 1 || time.Since(start) != 0 {
			t.Errorf("then %v after %d tries over %v", err, tries, time.Since(start))
		}
	})
}

// While the first dial on this network waits, Test, Save and copying each
// dial once and return at once, and the first dial keeps its retries.
func TestRetryLANOnlyFirstOfOverlapping(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := localNetworkRetry
		localNetworkRetry = newRetry()
		defer func() { localNetworkRetry = old }()
		e := newAppEnv(t, nil)
		// Test's dials: the first fails until its third try.
		var tests atomic.Int32
		e.s.test = func(ctx context.Context, endpoint string) ([]remote.Entry, error) {
			if tests.Add(1) < 3 {
				return nil, connectFailed(syscall.EPERM)
			}
			return []remote.Entry{{Path: "MyWorks", Kind: remote.Dir}}, nil
		}
		var reaches atomic.Int32
		e.s.reach = func(ctx context.Context, endpoint string) error {
			reaches.Add(1)
			return connectFailed(syscall.EPERM)
		}
		first := make(chan error, 1)
		start := time.Now()
		go func() {
			_, err := e.s.testLAN(t.Context(), "192.0.2.10:445")
			first <- err
		}()
		synctest.Wait() // the first dial is waiting to try again
		if err := e.m.Reach(t.Context(), "192.0.2.10:445"); err == nil || reaches.Load() != 1 || time.Since(start) != 0 {
			t.Errorf("Save while the first dial waits: %v after %d tries over %v", err, reaches.Load(), time.Since(start))
		}
		c := newCopyEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("abc")})
		c.dialErr = noRoute
		if err := c.c.start(c.set, Target{Endpoint: c.set.Address}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait() // copying waits for its next check
		if v := c.c.view(time.Now()); v.State != stUnreachable || c.dialCount() != 1 || time.Since(start) != 0 {
			t.Errorf("copying while the first dial waits: %d dials over %v, view %+v", c.dialCount(), time.Since(start), v)
		}
		c.c.stop()
		if err := <-first; err != nil || tests.Load() != 3 || time.Since(start) != 2*time.Second {
			t.Errorf("the first dial: %v after %d tries over %v", err, tests.Load(), time.Since(start))
		}
	})
}

// Test, from the page and from a route's Save, waits out the Local Network
// prompt on macOS, once: a later Test fails at once.
func TestTestWaitsForLocalNetwork(t *testing.T) {
	waited := setRetry(t, "darwin")
	e := newAppEnv(t, nil)
	tries := 0
	e.s.test = func(ctx context.Context, endpoint string) ([]remote.Entry, error) {
		if tries++; tries%3 != 0 {
			return nil, fmt.Errorf("connect to %s: %w", endpoint, connectFailed(syscall.EPERM))
		}
		return []remote.Entry{{Path: "MyWorks", Kind: remote.Dir}}, nil
	}
	e.s.reach = func(ctx context.Context, endpoint string) error {
		_, err := e.s.test(ctx, endpoint)
		return err
	}
	type answer struct {
		OK bool `json:"ok"`
	}
	if got := decode[answer](t, e.post("/copy/api/test", `{"address":"192.0.2.10"}`)); !got.OK || tries != 3 {
		t.Errorf("test = %+v after %d tries", got, tries)
	}
	if err := e.m.Reach(t.Context(), "192.0.2.10:445"); err == nil || tries != 4 {
		t.Errorf("Mount.Reach = %v after %d tries", err, tries)
	}
	if got := time.Duration(waited.Load()); got != 2*time.Second {
		t.Errorf("waited %v, want 2s", got)
	}
	setRetry(t, "darwin")
	tries = 0
	if err := e.m.Reach(t.Context(), "192.0.2.10:445"); err != nil || tries != 3 {
		t.Errorf("Mount.Reach first = %v after %d tries", err, tries)
	}
	// Once the wait runs out, Test says where to allow access.
	setHostOS(t, "darwin")
	waited = setRetry(t, "darwin")
	e.s.test = func(ctx context.Context, endpoint string) ([]remote.Entry, error) {
		return nil, fmt.Errorf("connect to %s: %w", endpoint, connectFailed(syscall.EPERM))
	}
	type failed struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if got := decode[failed](t, e.post("/copy/api/test", `{"address":"192.0.2.10"}`)); got.OK || !strings.HasSuffix(got.Message, lanHint) ||
		time.Duration(waited.Load()) != 4*time.Second {
		t.Errorf("test = %+v after %v", got, time.Duration(waited.Load()))
	}
}

// When the limit cuts a Test's try short, Test still says where to allow
// access.
func TestTestLimitKeepsHint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := localNetworkRetry
		localNetworkRetry = newRetry()
		defer func() { localNetworkRetry = old }()
		tries := 0
		s := &server{test: func(ctx context.Context, _ string) ([]remote.Entry, error) {
			if tries++; tries == 1 {
				return nil, noRoute
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		start := time.Now()
		_, err := s.testLAN(t.Context(), "192.0.2.10:445")
		if elapsed := time.Since(start); elapsed != 5*time.Second || tries != 2 || LocalNetworkHint("darwin", err) == "" {
			t.Errorf("%v after %d tries over %v", err, tries, elapsed)
		}
	})
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

// A change of where the telescope is takes back what failed where it was:
// a copy once that couldn't reach it, and a Free up space check.
func TestChangeClearsFailures(t *testing.T) {
	e := newAppEnv(t, savedAt(t.TempDir()))
	e.s.copy.mu.Lock()
	e.s.copy.st.state, e.s.copy.st.outcome, e.s.copy.st.endpoint = stDone, stUnreachable, testEndpoint
	e.s.copy.mu.Unlock()
	e.s.free.mu.Lock()
	e.s.free.st = freeStatus{state: frCheckFail, err: errors.New("connect to " + testEndpoint + ": no route")}
	e.s.free.mu.Unlock()
	e.move("192.0.2.20:4445")
	if v := e.s.copy.view(time.Now()); v.State != stOff {
		t.Errorf("copy after a change: %+v", v)
	}
	if v := e.s.free.view(); v.State != frStart || v.Changed {
		t.Errorf("free up space after a change: %+v", v)
	}
}

// Browse's view moves when what it shows does: a change of route or
// address, the target's endpoint changing or going, and a save that
// replaces Browse. Status alone, or a save that keeps Browse, leaves it.
func TestBrowseViewMoves(t *testing.T) {
	e := newAppEnv(t, nil)
	view := func() int {
		return decode[struct {
			BrowseView int `json:"browse_view"`
		}](t, e.serve("GET", "/copy/api/status", "")).BrowseView
	}
	if w := e.post("/copy/api/settings", goodInput); w.Code != 200 {
		t.Fatal(w.Code)
	}
	last := view()
	step := func(what string, moves bool) {
		t.Helper()
		got := view()
		if moved := got != last; moved != moves {
			t.Errorf("%s: view %d after %d, want moved %v", what, got, last, moves)
		}
		last = got
	}
	step("status again", false)
	e.post("/copy/api/settings", strings.Replace(goodInput, `"interval":"5"`, `"interval":"9"`, 1))
	step("an interval change", false)
	e.move("192.0.2.20:4445")
	step("a change of route", true)
	e.move("192.0.2.20:4445")
	step("a change to the same endpoint", true)
	e.mu.Lock()
	e.endpoint = "127.0.0.1:4445"
	e.mu.Unlock()
	step("a new endpoint without a change", true)
	e.mu.Lock()
	e.lost = errors.New("not connected")
	e.mu.Unlock()
	step("the target going", true)
	e.mu.Lock()
	e.lost = nil
	e.mu.Unlock()
	step("the target back", true)
	e.post("/copy/api/settings", strings.Replace(goodInput, quote(abs("/data/Seestar")), quote(abs("/data/Other")), 1))
	step("a new folder", true)
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
		localhttp.Respond(w, 200, tc.v)
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
