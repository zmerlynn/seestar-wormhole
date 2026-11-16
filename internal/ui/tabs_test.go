package ui

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/filelock"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// tabsClient drives a running app's shell and tabs over HTTP, as the
// shell and its pages do.
type tabsClient struct {
	t      *testing.T
	origin string
	token  string
	client *http.Client
}

func newTabsClient(t *testing.T, url string) *tabsClient {
	t.Helper()
	parts := strings.Split(url, "/#")
	c := &tabsClient{t: t, origin: parts[0], token: parts[1], client: &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}}
	t.Cleanup(c.client.CloseIdleConnections)
	return c
}

// do sends one request, and decodes the answer into v when it is set.
func (c *tabsClient) do(method, path, body string, v any) int {
	c.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, c.origin+path, reader)
	req.Header.Set("X-Seestar-Token", c.token)
	if method == "POST" {
		req.Header.Set("Origin", c.origin)
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil {
		if err := json.UnmarshalRead(resp.Body, v); err != nil {
			c.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

type tabsAnswer struct {
	Tabs     []tabView      `json:"tabs"`
	Closed   []profileEntry `json:"closed"`
	Profiles bool           `json:"profiles"`
	Pick     tabPick        `json:"pick"`
}

func (c *tabsClient) tabs() (v tabsAnswer) {
	c.t.Helper()
	if code := c.do("GET", "/api/tabs", "", &v); code != 200 {
		c.t.Fatalf("tabs: %d", code)
	}
	return v
}

func (v tabsAnswer) labels() (open, closed []string) {
	for _, t := range v.Tabs {
		open = append(open, t.Label)
	}
	for _, p := range v.Closed {
		closed = append(closed, p.Label)
	}
	return open, closed
}

// refusal is the message of a 400 answer to the shell's action.
func (c *tabsClient) refusal(path, body string) (int, string) {
	c.t.Helper()
	var refused struct {
		Message string `json:"message"`
	}
	code := c.do("POST", path, body, &refused)
	return code, refused.Message
}

// start starts the tab's run, as its page's Connect does.
func (c *tabsClient) start(tab string) {
	c.t.Helper()
	body, _ := json.Marshal(input{Settings: testSettings(), Password: testPassword})
	if code := c.do("POST", tab+"api/start", string(body), nil); code != 200 {
		c.t.Fatalf("start %s: %d", tab, code)
	}
}

// tabsHome keeps the copiers' history out of the real home.
func tabsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
}

// runTabs runs the app on root's profiles with a fake window, which
// drives it through drive and then closes it.
func runTabs(t *testing.T, root, profile string, run runFunc, drive func(c *tabsClient, activate <-chan struct{})) error {
	t.Helper()
	return runUIPresented(WithProfile(t.Context(), root, profile), "", io.Discard, io.Discard, run, func(string) error { t.Error("opened browser"); return nil }, &fakeKeychain{}, func(_ context.Context, url string, closeWindow func(), activate <-chan struct{}) error {
		drive(newTabsClient(t, url), activate)
		closeWindow()
		return nil
	}, &copyapp.Native{})
}

// heldLock holds the profile's ui.lock as another process would, until
// the test ends.
func heldLock(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ui.lock")
	f, err := privatefile.Open(path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if ok, err := filelock.TryLock(f); err != nil || !ok {
		t.Fatal("lock", ok, err)
	}
	return path
}

// lockFree is whether the profile's ui.lock can be taken now.
func lockFree(t *testing.T, dir string) bool {
	t.Helper()
	f, err := privatefile.Open(filepath.Join(dir, "ui.lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ok, err := filelock.TryLock(f)
	return err == nil && ok
}

// waitFor polls cond for up to two seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out:", what)
		}
	}
}

// One app holds a controller per open profile: tabs open from + in
// order, each holding its folder's lock and recording its own path;
// closing one stops its run and releases its lock; it reopens; and
// closing the last tab quits the app.
func TestTabsOpenCloseReopen(t *testing.T) {
	tabsHome(t)
	root := filepath.Join(t.TempDir(), "state")
	if _, err := CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProfile(root, "Backyard"); err != nil {
		t.Fatal(err)
	}
	started, stopped := make(chan struct{}, 4), make(chan string, 4)
	run := func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status, _ func(proxy.Dialer) (func(), bool)) error {
		started <- struct{}{}
		<-ctx.Done()
		stopped <- RunProfile(ctx)
		return nil
	}
	quitted := make(chan struct{})
	err := runTabs(t, root, "", run, func(c *tabsClient, _ <-chan struct{}) {
		// A plain launch opens Default only.
		v := c.tabs()
		if open, closed := v.labels(); !slices.Equal(open, []string{"Default"}) || !slices.Equal(closed, []string{"Backyard", "Garden"}) || !v.Profiles || v.Tabs[0].Path != "/p/1/" {
			t.Fatalf("launch: %v %v %+v", open, closed, v)
		}
		var opened struct {
			Path string `json:"path"`
		}
		if code := c.do("POST", "/api/tabs/open", `{"name":"garden"}`, &opened); code != 200 || opened.Path != "/p/2/" {
			t.Fatalf("open Garden: %d %q", code, opened.Path)
		}
		v = c.tabs()
		if open, closed := v.labels(); !slices.Equal(open, []string{"Default", "Garden"}) || !slices.Equal(closed, []string{"Backyard"}) || v.Pick.Path != "/p/2/" {
			t.Fatalf("opened: %v %v %+v", open, closed, v.Pick)
		}
		garden := ProfileDir(root, "Garden")
		if lockFree(t, garden) {
			t.Fatal("Garden's lock isn't held")
		}
		var record runtimeRecord
		if err := (&storage{root: garden}).read("ui-runtime.json", &record); err != nil || record.Path != "/p/2/" || record.Token != c.token {
			t.Fatalf("Garden's record: %+v %v", record, err)
		}
		// Each tab's page answers at its own path with its own settings.
		var status struct {
			Active bool `json:"active"`
		}
		if code := c.do("GET", "/p/2/api/status", "", &status); code != 200 {
			t.Fatalf("Garden's status: %d", code)
		}
		if code := c.do("GET", "/p/9/api/status", "", nil); code != 404 {
			t.Fatalf("no tab: %d", code)
		}
		// Closing a running tab stops its run and releases its lock, and
		// answers with the tabs left.
		c.start("/p/2/")
		<-started
		var left tabsAnswer
		if code := c.do("POST", "/api/tabs/close", `{"path":"/p/2/"}`, &left); code != 200 {
			t.Fatalf("close: %d", code)
		}
		if open, closed := left.labels(); !slices.Equal(open, []string{"Default"}) || !slices.Contains(closed, "Garden") {
			t.Fatalf("close answered: %v %v", open, closed)
		}
		select {
		case p := <-stopped:
			if p != "Garden" {
				t.Fatalf("stopped %q", p)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Garden's run didn't stop")
		}
		if !lockFree(t, garden) {
			t.Fatal("Garden's lock is still held")
		}
		if _, err := os.Stat(filepath.Join(garden, "ui-runtime.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("Garden's record remains", err)
		}
		if open, closed := c.tabs().labels(); !slices.Equal(open, []string{"Default"}) || !slices.Contains(closed, "Garden") {
			t.Fatalf("closed: %v %v", open, closed)
		}
		if code := c.do("GET", "/p/2/api/status", "", nil); code != 404 {
			t.Fatalf("a closed tab answers: %d", code)
		}
		// Reopened, it is a new tab.
		if code := c.do("POST", "/api/tabs/open", `{"name":"Garden"}`, &opened); code != 200 || opened.Path != "/p/3/" {
			t.Fatalf("reopen: %d %q", code, opened.Path)
		}
		// New profile creates and opens one, and refuses a name in use.
		if code, message := c.refusal("/api/tabs/new", `{"name":"backyard"}`); code != 400 || !strings.Contains(message, "Backyard") {
			t.Fatalf("new in use: %d %q", code, message)
		}
		if code := c.do("POST", "/api/tabs/new", `{"name":"Porch"}`, &opened); code != 200 || opened.Path != "/p/4/" {
			t.Fatalf("new: %d %q", code, opened.Path)
		}
		if _, err := os.Stat(ProfileDir(root, "Porch")); err != nil {
			t.Fatal(err)
		}
		// Closing the last tab quits the app, and answers no tabs.
		for _, path := range []string{"/p/1/", "/p/3/", "/p/4/"} {
			c.do("POST", "/api/tabs/close", `{"path":"`+path+`"}`, &left)
		}
		if len(left.Tabs) != 0 {
			t.Fatalf("the last close answered %+v", left.Tabs)
		}
		close(quitted)
	})
	if err != nil {
		t.Fatal(err)
	}
	<-quitted
	for _, name := range []string{DefaultProfile, "Garden", "Porch"} {
		if !lockFree(t, ProfileDir(root, name)) {
			t.Error(name, "is still locked")
		}
	}
}

// A launch that names a profile asks the running app to open its tab,
// or select it, and to show the window; a profile another process holds
// is refused with the real error, and stays held.
func TestTabsLaunchAsksRunningApp(t *testing.T) {
	tabsHome(t)
	root := filepath.Join(t.TempDir(), "state")
	for _, name := range []string{"Garden", "Backyard"} {
		if _, err := CreateProfile(root, name); err != nil {
			t.Fatal(err)
		}
	}
	held := heldLock(t, ProfileDir(root, "Backyard"))
	err := runTabs(t, root, "", nil, func(c *tabsClient, activate <-chan struct{}) {
		relaunch := func(profile string) error {
			return runUIPresented(WithProfile(t.Context(), root, profile), "", io.Discard, io.Discard, nil, func(string) error { t.Error("relaunch opened browser"); return nil }, &fakeKeychain{}, func(context.Context, string, func(), <-chan struct{}) error {
				t.Error("relaunch opened a window")
				return nil
			}, &copyapp.Native{})
		}
		shown := func(what string) {
			t.Helper()
			select {
			case <-activate:
			case <-time.After(time.Second):
				t.Fatal("not shown:", what)
			}
		}
		// A plain relaunch brings the window forward.
		if err := relaunch(""); err != nil {
			t.Fatal(err)
		}
		shown("relaunch")
		// ui --profile Garden opens its tab and selects it.
		if err := relaunch("garden"); err != nil {
			t.Fatal(err)
		}
		shown("Garden")
		v := c.tabs()
		if open, _ := v.labels(); !slices.Equal(open, []string{"Default", "Garden"}) || v.Pick.Path != "/p/2/" {
			t.Fatalf("Garden: %v %+v", open, v.Pick)
		}
		// Asked again, Default is selected, not opened twice.
		if err := relaunch("Default"); err != nil {
			t.Fatal(err)
		}
		shown("Default")
		v2 := c.tabs()
		if open, _ := v2.labels(); !slices.Equal(open, []string{"Default", "Garden"}) || v2.Pick.Path != "/p/1/" || v2.Pick.Seq != v.Pick.Seq+1 {
			t.Fatalf("Default: %v %+v", open, v2.Pick)
		}
		// Backyard is held by another process: the launch, + and an
		// activation all refuse with the lock's real error.
		want := "Couldn't open Backyard: Backyard is open in another seestar-wormhole window: its lock, " + held + ", is held."
		if err := relaunch("Backyard"); err == nil || err.Error() != want {
			t.Fatalf("launch: %v", err)
		}
		if code, message := c.refusal("/api/tabs/open", `{"name":"Backyard"}`); code != 400 || message != want {
			t.Fatalf("+: %d %q", code, message)
		}
		if open, _ := c.tabs().labels(); len(open) != 2 {
			t.Fatalf("a refused profile opened: %v", open)
		}
		if lockFree(t, ProfileDir(root, "Backyard")) {
			t.Fatal("the other process's lock was taken over")
		}
		// A missing profile is refused as FindProfile finds it.
		if err := relaunch("Porch"); err == nil || !strings.HasPrefix(err.Error(), "Couldn't open Porch: ") {
			t.Fatalf("missing: %v", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

// olderApp answers in dir as an app of an older build does: its window
// shows on an activation, which it counts, and it has no tabs.
func olderApp(t *testing.T, dir string) *atomic.Int32 {
	t.Helper()
	var activated atomic.Int32
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/instance":
			w.Header().Set("X-Seestar-Window", "1")
			io.WriteString(w, `{"instance":"seestar-wormhole-ui-v1"}`)
		case "/api/activate":
			activated.Add(1)
			io.WriteString(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(old.Close)
	port, _ := strconv.Atoi(old.URL[strings.LastIndexByte(old.URL, ':')+1:])
	st, err := openStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.directory.Close()
	if err := st.write("ui-runtime.json", runtimeRecord{Port: port, Token: strings.Repeat("ab", 32)}); err != nil {
		t.Fatal(err)
	}
	return &activated
}

// A launch of a profile another process holds, with no app of its own
// to ask, refuses with the real error, even when that process is an
// older build that answers: it can't open the profile's tab.
func TestTabsLaunchLockedByAnotherProcess(t *testing.T) {
	tabsHome(t)
	root := filepath.Join(t.TempDir(), "state")
	if _, err := CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	held := heldLock(t, ProfileDir(root, "Garden"))
	activated := olderApp(t, ProfileDir(root, "Garden"))
	ctx, cancel := context.WithTimeout(WithProfile(t.Context(), root, "Garden"), 5*time.Second)
	defer cancel()
	err := runUIPresented(ctx, "", io.Discard, io.Discard, nil, func(string) error { t.Error("opened browser"); return nil }, &fakeKeychain{}, func(context.Context, string, func(), <-chan struct{}) error {
		t.Error("opened a window")
		return nil
	}, &copyapp.Native{})
	if want := "Couldn't open Garden: Garden is open in another seestar-wormhole window: its lock, " + held + ", is held."; err == nil || err.Error() != want {
		t.Fatal(err)
	}
	if n := activated.Load(); n != 0 {
		t.Fatalf("the older app was activated %d times", n)
	}
}

// A launch that names a profile asks only an app that can open its tab:
// an older build's app, or an app of one --state-dir folder, answering
// in a profile's folder is passed over, for another app with tabs or
// for an app of its own.
func TestTabsLaunchSkipsAppsWithoutProfiles(t *testing.T) {
	tabsHome(t)
	root := filepath.Join(t.TempDir(), "state")
	for _, name := range []string{"Garden", "Backyard", "Porch"} {
		if _, err := CreateProfile(root, name); err != nil {
			t.Fatal(err)
		}
	}
	held := heldLock(t, ProfileDir(root, "Porch"))
	older := olderApp(t, ProfileDir(root, "Porch"))
	launch := func(profile string, window windowFunc) error {
		return runUIPresented(WithProfile(t.Context(), root, profile), "", io.Discard, io.Discard, nil, func(string) error { t.Error("opened browser"); return nil }, &fakeKeychain{}, window, &copyapp.Native{})
	}
	noWindow := func(context.Context, string, func(), <-chan struct{}) error {
		t.Error("the launch opened a window")
		return nil
	}
	// An app of the root as one --state-dir folder holds Default's folder.
	err := runUIPresented(t.Context(), root, io.Discard, io.Discard, nil, func(string) error { t.Error("opened browser"); return nil }, &fakeKeychain{}, func(_ context.Context, url string, closeWindow func(), isolated <-chan struct{}) error {
		defer closeWindow()
		// It refuses to stand in for a profile asked of it directly.
		if code, message := newTabsClient(t, url).refusal("/api/activate", `{"profile":"Garden"}`); code != 400 || !strings.HasPrefix(message, "Couldn't open Garden: ") {
			t.Errorf("isolated activation: %d %q", code, message)
		}
		// With no app with tabs running, Garden's launch opens its own.
		opened := false
		if err := launch("Garden", func(_ context.Context, url string, closeWindow func(), _ <-chan struct{}) error {
			opened = true
			if open, _ := newTabsClient(t, url).tabs().labels(); !slices.Equal(open, []string{"Garden"}) {
				t.Errorf("Garden's own app: %v", open)
			}
			closeWindow()
			return nil
		}); err != nil || !opened {
			t.Errorf("Garden's launch: %v, opened its window: %t", err, opened)
		}
		// With an app with tabs running, Garden opens there, and a
		// profile the older app holds is refused by its lock.
		return runTabs(t, root, "Backyard", nil, func(c *tabsClient, activate <-chan struct{}) {
			if err := launch("Garden", noWindow); err != nil {
				t.Fatal(err)
			}
			<-activate
			if open, _ := c.tabs().labels(); !slices.Equal(open, []string{"Backyard", "Garden"}) {
				t.Fatalf("Garden: %v", open)
			}
			want := "Couldn't open Porch: Porch is open in another seestar-wormhole window: its lock, " + held + ", is held."
			if err := launch("Porch", noWindow); err == nil || err.Error() != want {
				t.Fatalf("Porch: %v", err)
			}
			select {
			case <-isolated:
				t.Error("the --state-dir app was activated")
			default:
			}
			if n := older.Load(); n != 0 {
				t.Errorf("the older app was activated %d times", n)
			}
		})
	}, &copyapp.Native{})
	if err != nil {
		t.Fatal(err)
	}
}

// The first tab whose run binds the ports holds them; another tab's run
// is copy-only and names the holder tab; closing the holder leaves the
// copy-only tab working; and the CLI's probe answers per profile.
func TestTabsCopyOnlyNamesHolder(t *testing.T) {
	tabsHome(t)
	root := filepath.Join(t.TempDir(), "state")
	if _, err := CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, s Settings, _ cloud.Credentials, status *proxy.Status, _ func(proxy.Dialer) (func(), bool)) error {
		if h, held := PortHolder(ctx, s.Bind, 80); held {
			status.MarkCopyOnly(h)
		} else {
			status.MarkServing(s.Bind)
		}
		<-ctx.Done()
		status.ClearServing()
		return nil
	}
	err := runTabs(t, root, "", run, func(c *tabsClient, _ <-chan struct{}) {
		type statusAnswer struct {
			Active       bool          `json:"active"`
			ServingPorts []uint16      `json:"serving_ports"`
			CopyOnly     *proxy.Holder `json:"copy_only"`
		}
		status := func(tab string) (v statusAnswer) {
			t.Helper()
			if code := c.do("GET", tab+"api/status", "", &v); code != 200 {
				t.Fatalf("%s status: %d", tab, code)
			}
			return v
		}
		c.start("/p/1/")
		waitFor(t, "Default serving", func() bool { return slices.Contains(status("/p/1/").ServingPorts, 80) })
		c.do("POST", "/api/tabs/open", `{"name":"Garden"}`, nil)
		c.start("/p/2/")
		waitFor(t, "Garden copy-only", func() bool { return status("/p/2/").CopyOnly != nil })
		if got := status("/p/2/"); *got.CopyOnly != (proxy.Holder{Profile: "Default", Tab: true}) || len(got.ServingPorts) != 0 {
			t.Fatalf("Garden: %+v %+v", got, *got.CopyOnly)
		}
		// The CLI's probe reads each profile's own tab.
		for _, self := range []string{"Garden", "default"} {
			w, ok, err := EndpointHolder(t.Context(), root, self, "0.0.0.0", 80)
			if err != nil || !ok || w.Profile != "Default" || w.Own != (self == "default") {
				t.Errorf("EndpointHolder from %s = %+v, %t, %v", self, w, ok, err)
			}
		}
		if _, ok, _ := EndpointHolder(t.Context(), root, "Garden", "0.0.0.0", 445); ok {
			t.Error("port 445 has a holder")
		}
		// An app's own tabs aren't another app's windows.
		if h, ok := ServingWindow(t.Context(), root, "Garden", "0.0.0.0", 80, nil); !ok || h.Profile != "Default" || h.Tab {
			t.Errorf("ServingWindow = %+v, %t", h, ok)
		}
		// Closing the holder leaves Garden running copy-only.
		c.do("POST", "/api/tabs/close", `{"path":"/p/1/"}`, nil)
		if got := status("/p/2/"); !got.Active || got.CopyOnly == nil || got.CopyOnly.Profile != "Default" {
			t.Fatalf("Garden after the holder closed: %+v", got)
		}
		if _, ok, _ := EndpointHolder(t.Context(), root, "Garden", "0.0.0.0", 80); ok {
			t.Error("a closed tab still holds the port")
		}
		if open, _ := c.tabs().labels(); !slices.Equal(open, []string{"Garden"}) {
			t.Fatalf("open: %v", open)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The shell's assets and API carry the local controls' headers, and the
// shell's API takes only the app's token, host and origin.
func TestTabsShellHeadersAndChecks(t *testing.T) {
	tabsHome(t)
	root := filepath.Join(t.TempDir(), "state")
	err := runTabs(t, root, "", nil, func(c *tabsClient, _ <-chan struct{}) {
		for _, path := range []string{"/", "/shell.js", "/style.css", "/icon.svg", "/api/tabs", "/api/instance", "/p/1/", "/p/1/app.js", "/p/1/api/status"} {
			req, _ := http.NewRequest("GET", c.origin+path, nil)
			req.Header.Set("X-Seestar-Token", c.token)
			resp, err := c.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'self'") || resp.Header.Get("Cache-Control") == "" {
				t.Errorf("%s: %d %q", path, resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
			}
		}
		post := func(path string, change func(*http.Request)) int {
			req, _ := http.NewRequest("POST", c.origin+path, strings.NewReader(`{"name":"Garden"}`))
			req.Header.Set("X-Seestar-Token", c.token)
			req.Header.Set("Origin", c.origin)
			req.Header.Set("Content-Type", "application/json")
			change(req)
			resp, err := c.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			return resp.StatusCode
		}
		for what, change := range map[string]func(*http.Request){
			"token":  func(r *http.Request) { r.Header.Set("X-Seestar-Token", strings.Repeat("0", 64)) },
			"origin": func(r *http.Request) { r.Header.Set("Origin", "http://invalid.example") },
			"site":   func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
			"host":   func(r *http.Request) { r.Host = "localhost" },
			"type":   func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		} {
			for _, path := range []string{"/api/tabs/new", "/p/1/api/save"} {
				if code := post(path, change); code == 200 {
					t.Errorf("%s admitted without its %s", path, what)
				}
			}
		}
		if _, err := os.Stat(ProfileDir(root, "Garden")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a refused request created Garden", err)
		}
		if code := c.do("GET", "/api/tabs/new", "", nil); code != 405 {
			t.Errorf("GET of an action: %d", code)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

// --state-dir is one folder: one tab, labeled by the folder, with no +.
func TestTabsStateDir(t *testing.T) {
	tabsHome(t)
	dir := filepath.Join(t.TempDir(), "Observatory")
	err := runUIPresented(t.Context(), dir, io.Discard, io.Discard, nil, func(string) error { t.Error("opened browser"); return nil }, &fakeKeychain{}, func(_ context.Context, url string, closeWindow func(), _ <-chan struct{}) error {
		c := newTabsClient(t, url)
		v := c.tabs()
		if open, closed := v.labels(); !slices.Equal(open, []string{"Observatory"}) || len(closed) != 0 || v.Profiles {
			t.Errorf("state dir: %v %v %+v", open, closed, v)
		}
		if code := c.do("POST", "/api/tabs/open", `{"name":"Garden"}`, nil); code != 404 {
			t.Errorf("open in a state dir: %d", code)
		}
		closeWindow()
		return nil
	}, &copyapp.Native{})
	if err != nil {
		t.Fatal(err)
	}
}

// gateApp is an app on a fresh root's profiles, with Garden made beside
// Default and Default open, served by no listener: its tabs open, close
// and quit as the shell's actions would.
func gateApp(t *testing.T, run runFunc) (*app, *tab) {
	t.Helper()
	tabsHome(t)
	a, listener, err := newApp(t.Context(), t.Context(), filepath.Join(t.TempDir(), "state"), true, io.Discard, run, &fakeKeychain{}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	t.Cleanup(a.closeAll)
	if _, err := CreateProfile(a.root, "Garden"); err != nil {
		t.Fatal(err)
	}
	first, err := a.showCreated(DefaultProfile, "")
	if err != nil {
		t.Fatal(err)
	}
	return a, first
}

// heldRuns are runs that say on started that they started and, once
// their context ends, on stopping that they are stopping; each returns
// only when release sends, or the test ends.
type heldRuns struct{ started, stopping, release chan struct{} }

func newHeldRuns(t *testing.T) (*heldRuns, runFunc) {
	h := &heldRuns{make(chan struct{}, 4), make(chan struct{}, 4), make(chan struct{}, 4)}
	return h, func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status, _ func(proxy.Dialer) (func(), bool)) error {
		h.started <- struct{}{}
		<-ctx.Done()
		h.stopping <- struct{}{}
		select {
		case <-h.release:
		case <-t.Context().Done():
		}
		return nil
	}
}

// Closing the last tab quits the app: an open asked for while it closes
// is refused rather than left running in a quitting app.
func TestTabsOpenDuringLastClose(t *testing.T) {
	runs, run := newHeldRuns(t)
	a, first := gateApp(t, run)
	if err := first.server.controller.start(testSettings(), testPassword); err != nil {
		t.Fatal(err)
	}
	<-runs.started
	closed := make(chan struct{})
	go func() { first.close(); close(closed) }()
	<-runs.stopping
	_, err := a.showCreated("Garden", "")
	runs.release <- struct{}{}
	<-closed
	if err == nil || !strings.Contains(err.Error(), "quitting") {
		t.Fatalf("Garden opened as the last tab closed: %v", err)
	}
	select {
	case <-a.quit:
	default:
		t.Fatal("closing the last tab didn't quit")
	}
	if len(a.open()) != 0 || !lockFree(t, ProfileDir(a.root, "Garden")) {
		t.Fatalf("Garden is open: %d tabs", len(a.open()))
	}
}

// An open under way when the app quits is joined by the quit and never
// published: its lock is released when the quit returns.
func TestTabsOpenDuringQuit(t *testing.T) {
	a, _ := gateApp(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	saved := newMount
	t.Cleanup(func() { newMount = saved })
	newMount = func(ctx context.Context, o copyapp.MountOptions) *copyapp.Mount {
		close(entered)
		<-release
		return saved(ctx, o)
	}
	result := make(chan error, 1)
	go func() {
		_, err := a.showCreated("Garden", "")
		result <- err
	}()
	<-entered
	quit := make(chan struct{})
	go func() { a.closeAll(); close(quit) }()
	waitFor(t, "quitting", a.quitting.Load)
	close(release)
	<-quit
	if !lockFree(t, ProfileDir(a.root, "Garden")) || len(a.open()) != 0 {
		t.Fatalf("Garden outlived the quit: %d tabs", len(a.open()))
	}
	if err := <-result; err == nil || !strings.Contains(err.Error(), "quitting") {
		t.Fatalf("open during quit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ProfileDir(a.root, "Garden"), "ui-runtime.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Garden's record remains", err)
	}
}

// A profile reopened while its tab closes opens again once the close
// has released its lock, rather than being refused as another
// process's; a quit meanwhile joins the close under way.
func TestTabsReopenDuringClose(t *testing.T) {
	runs, run := newHeldRuns(t)
	a, _ := gateApp(t, run)
	garden, err := a.showCreated("Garden", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := garden.server.controller.start(testSettings(), testPassword); err != nil {
		t.Fatal(err)
	}
	<-runs.started
	closed := make(chan struct{})
	go func() { garden.close(); close(closed) }()
	<-runs.stopping
	if v := a.tabsView(); len(v.Tabs) != 1 || v.Closed[0].Name != "Garden" {
		t.Fatalf("a closing tab is shown: %+v", v)
	}
	type opened struct {
		t   *tab
		err error
	}
	reopened := make(chan opened, 1)
	go func() {
		t, err := a.showCreated("Garden", "")
		reopened <- opened{t, err}
	}()
	select {
	case r := <-reopened:
		t.Fatalf("reopened before the close finished: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	runs.release <- struct{}{}
	<-closed
	r := <-reopened
	if r.err != nil || r.t == garden || r.t.path != "/p/3/" || lockFree(t, garden.dir) {
		t.Fatalf("reopen: %+v", r)
	}

	// A quit joins a close under way: it returns only once that tab's
	// run has stopped and its lock is released.
	if err := r.t.server.controller.start(testSettings(), testPassword); err != nil {
		t.Fatal(err)
	}
	<-runs.started
	go r.t.close()
	<-runs.stopping
	quit := make(chan struct{})
	go func() { a.closeAll(); close(quit) }()
	select {
	case <-quit:
		t.Fatal("the quit left a closing tab behind")
	case <-time.After(50 * time.Millisecond):
	}
	runs.release <- struct{}{}
	<-quit
	if !lockFree(t, garden.dir) || !lockFree(t, ProfileDir(a.root, DefaultProfile)) {
		t.Fatal("a lock outlived the quit")
	}
}

// blockedKeychain holds a run's start in reading the saved password: it
// says so on entered, and answers once release is closed.
type blockedKeychain struct {
	fakeKeychain
	entered, release chan struct{}
}

func (b *blockedKeychain) Get(string, string) (string, error) {
	close(b.entered)
	<-b.release
	return testPassword, nil
}

// A tab closed while its run starts, before the run is entered, stops
// starting, releases its lock and reopens.
func TestTabsCloseWhileStarting(t *testing.T) {
	a, _ := gateApp(t, func(context.Context, Settings, cloud.Credentials, *proxy.Status, func(proxy.Dialer) (func(), bool)) error {
		t.Error("the run was entered")
		return nil
	})
	chain := &blockedKeychain{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(chain.release) })
	a.chain = chain
	garden, err := a.showCreated("Garden", "")
	if err != nil {
		t.Fatal(err)
	}
	s := testSettings()
	s.Remember = true
	if err := garden.server.controller.start(s, ""); err != nil {
		t.Fatal(err)
	}
	<-chain.entered
	closed := make(chan struct{})
	go func() { garden.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("closing a starting tab didn't finish")
	}
	if garden.server.controller.view().Active || !lockFree(t, garden.dir) {
		t.Fatal("the starting tab outlived its close")
	}
	if _, err := os.Stat(filepath.Join(garden.dir, "ui-runtime.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Garden's record remains", err)
	}
	if again, err := a.showCreated("Garden", ""); err != nil || again.path != "/p/3/" {
		t.Fatalf("reopen: %v", err)
	}
}

// post sends the shell's action to the app as its page would.
func post(a *app, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://"+a.host+path, strings.NewReader(body))
	r.Header.Set("X-Seestar-Token", a.token)
	r.Header.Set("Origin", "http://"+a.host)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

// Two asks to open one profile at once, by + and by a launch's
// activation, open it once: the second waits for the first and selects
// the same tab, with its one controller and lock.
func TestTabsConcurrentOpens(t *testing.T) {
	a, _ := gateApp(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	var mounts atomic.Int32
	saved := newMount
	t.Cleanup(func() { newMount = saved })
	newMount = func(ctx context.Context, o copyapp.MountOptions) *copyapp.Mount {
		if mounts.Add(1) == 1 {
			close(entered)
			<-release
		}
		return saved(ctx, o)
	}
	opened, activated := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { opened <- post(a, "/api/tabs/open", `{"name":"Garden"}`) }()
	<-entered
	go func() { activated <- post(a, "/api/activate", `{"profile":"garden"}`) }()
	select {
	case w := <-activated:
		t.Fatalf("the activation answered before the open finished: %d %s", w.Code, w.Body)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	o, act := <-opened, <-activated
	if o.Code != 200 || act.Code != 200 || !strings.Contains(o.Body.String(), `"/p/2/"`) {
		t.Fatalf("open %d %s, activation %d %s", o.Code, o.Body, act.Code, act.Body)
	}
	v := a.tabsView()
	if open, _ := (tabsAnswer{Tabs: v.Tabs}).labels(); !slices.Equal(open, []string{"Default", "Garden"}) || v.Pick.Path != "/p/2/" || v.Pick.Seq != 3 || mounts.Load() != 1 {
		t.Fatalf("tabs %v, pick %+v, mounts %d", open, v.Pick, mounts.Load())
	}
	if lockFree(t, ProfileDir(a.root, "Garden")) {
		t.Fatal("Garden's lock isn't held")
	}
}

// Each tab's run has its own profile's folder as its state folder, where
// the proxy keeps its installation's identity, and its label; Default's
// is the root.
func TestTabsRunDir(t *testing.T) {
	type ran struct{ dir, profile string }
	runs := make(chan ran, 2)
	a, first := gateApp(t, func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status, _ func(proxy.Dialer) (func(), bool)) error {
		runs <- ran{RunDir(ctx, "fallback"), RunProfile(ctx)}
		<-ctx.Done()
		return nil
	})
	garden, err := a.showCreated("Garden", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tab := range []*tab{first, garden} {
		if err := tab.server.controller.start(testSettings(), testPassword); err != nil {
			t.Fatal(err)
		}
		if r := <-runs; r != (ran{tab.dir, tab.label}) {
			t.Errorf("%s ran with %+v", tab.label, r)
		}
	}
	if first.dir != a.root || garden.dir != ProfileDir(a.root, "Garden") {
		t.Errorf("folders %q %q", first.dir, garden.dir)
	}
	if dir := RunDir(t.Context(), "fallback"); dir != "fallback" || RunProfile(t.Context()) != "" {
		t.Errorf("no tab: %q", dir)
	}
}
