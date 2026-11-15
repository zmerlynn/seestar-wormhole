package ui

import (
	"encoding/json/v2"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeProfileWindow answers as the open window of the state folder dir,
// and lists the bodies of the activations it receives.
func fakeProfileWindow(t *testing.T, dir string) func() []string {
	t.Helper()
	var mu sync.Mutex
	var activations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Seestar-Token") != strings.Repeat("cd", 32) {
			http.Error(w, "Request rejected", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/status":
			io.WriteString(w, `{"serving_ports":[]}`)
		case "/api/activate":
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			activations = append(activations, string(b))
			mu.Unlock()
			io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	st, err := openStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.directory.Close()
	if err := st.write("ui-runtime.json", runtimeRecord{Port: server.Listener.Addr().(*net.TCPAddr).Port, Token: strings.Repeat("cd", 32)}); err != nil {
		t.Fatal(err)
	}
	return func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(activations) }
}

// profileServer is a test server of the root's profile name.
func profileServer(t *testing.T, root, name string) *server {
	s := testServer(t)
	s.profile = &runProfile{root, name}
	return s
}

// replaceStartProfile records the profiles started instead of starting
// them.
func replaceStartProfile(t *testing.T) *[]string {
	var started []string
	old := startProfile
	t.Cleanup(func() { startProfile = old })
	startProfile = func(name string) error { started = append(started, name); return nil }
	return &started
}

// The dialog lists Default first and then the named profiles, marks this
// window's and the open ones, brings an open one forward without asking,
// and starts a closed one.
func TestProfilesDialog(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	for _, name := range []string{"Garden", "Backyard"} {
		if _, err := CreateProfile(root, name); err != nil {
			t.Fatal(err)
		}
	}
	activations := fakeProfileWindow(t, ProfileDir(root, "Garden"))
	started := replaceStartProfile(t)
	s := profileServer(t, root, DefaultProfile)

	w := httptest.NewRecorder()
	s.ServeHTTP(w, request(s, "GET", "/api/profiles", ""))
	var listed struct {
		Profiles []profileEntry `json:"profiles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); w.Code != 200 || err != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	want := []profileEntry{{"default", "Default", true, false}, {"Backyard", "Backyard", false, false}, {"Garden", "Garden", false, true}}
	if !slices.Equal(listed.Profiles, want) {
		t.Errorf("listed %+v", listed.Profiles)
	}

	for _, name := range []string{"garden", "Backyard", "Default"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, request(s, "POST", "/api/profiles/open", `{"name":"`+name+`"}`))
		if w.Code != 200 {
			t.Errorf("open %s: %d %s", name, w.Code, w.Body)
		}
	}
	if got := activations(); !slices.Equal(got, []string{`{"show":true}`}) {
		t.Errorf("Garden's activations %q", got)
	}
	if !slices.Equal(*started, []string{"Backyard"}) {
		t.Errorf("started %q", *started)
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, request(s, "POST", "/api/profiles/open", `{"name":"Orchard"}`))
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"profile_open"`) || !strings.Contains(w.Body.String(), "Couldn't open Orchard: no profile named Orchard") {
		t.Errorf("open Orchard: %d %s", w.Code, w.Body)
	}

	// A profile without a window isn't asked for one: no profile, no
	// dialog.
	plain := testServer(t)
	w = httptest.NewRecorder()
	plain.ServeHTTP(w, request(plain, "GET", "/api/profiles", ""))
	if w.Code != 404 {
		t.Errorf("no profile: %d", w.Code)
	}
}

// New profile trims the name, shows a refusal as is, and opens what it
// created.
func TestNewProfile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if _, err := CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	started := replaceStartProfile(t)
	s := profileServer(t, root, "Garden")
	for _, tc := range []struct{ typed, message string }{
		{"  ", "Enter a name."},
		{"default", "Default already exists. Choose another name."},
		{"garden", "There's already a profile named Garden."},
		{"Back/yard", `A name can't contain / or \\.`},
	} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, request(s, "POST", "/api/profiles/new", `{"name":"`+tc.typed+`"}`))
		if w.Code != 400 || strings.TrimSpace(w.Body.String()) != `{"code":"profile_name","message":"`+tc.message+`"}` {
			t.Errorf("%q: %d %s", tc.typed, w.Code, w.Body)
		}
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request(s, "POST", "/api/profiles/new", `{"name":"  Backyard S30 "}`))
	if w.Code != 200 || !slices.Equal(*started, []string{"Backyard S30"}) {
		t.Errorf("%d %s, started %q", w.Code, w.Body, *started)
	}
	if info, err := os.Stat(ProfileDir(root, "Backyard S30")); err != nil || !info.IsDir() {
		t.Errorf("no folder: %v", err)
	}
}

// A relaunch asks which profile to open, on the page's first status only,
// on Windows and Linux once a named profile exists; never on macOS or for
// another window's Profiles. The label is Default's only once there are
// others, and the window's title follows it.
func TestRelaunchAsksForProfile(t *testing.T) {
	old := hostOS
	t.Cleanup(func() { hostOS = old })
	root := filepath.Join(t.TempDir(), "state")
	s := profileServer(t, root, DefaultProfile)
	s.activate = func() {}
	var titles []string
	s.title = &windowTitle{text: titleFor("")}
	s.title.attach(func(text string) { titles = append(titles, text) })
	status := func(path string) (v *profileView) {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, request(s, "GET", path, ""))
		var got struct {
			Profile *profileView `json:"profile"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Profile
	}
	relaunch := func(body string) {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, request(s, "POST", "/api/activate", body))
		if w.Code != 200 {
			t.Fatalf("activate %s: %d", body, w.Code)
		}
	}

	hostOS = "linux"
	relaunch("{}")
	if v := status("/api/status?first=1"); v == nil || v.Label != "" || v.Ask {
		t.Errorf("only Default: %+v", v)
	}
	if _, err := CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	if v := status("/api/status"); v.Label != "Default" || v.Ask {
		t.Errorf("with Garden: %+v", v)
	}
	if !slices.Equal(titles, []string{"seestar-wormhole — Default"}) {
		t.Errorf("titles %q", titles)
	}
	for _, tc := range []struct {
		goos, body string
		ask        bool
	}{{"linux", "{}", true}, {"windows", "{}", true}, {"darwin", "{}", false}, {"linux", `{"show":true}`, false}} {
		hostOS = tc.goos
		relaunch(tc.body)
		if v := status("/api/status"); v.Ask {
			t.Errorf("%s %s: a later poll took the question", tc.goos, tc.body)
		}
		if v := status("/api/status?first=1"); v.Ask != tc.ask {
			t.Errorf("%s %s: asked %t", tc.goos, tc.body, v.Ask)
		}
		if v := status("/api/status?first=1"); v.Ask {
			t.Errorf("%s %s: asked twice", tc.goos, tc.body)
		}
	}

	g := profileServer(t, root, "Garden")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, request(g, "GET", "/api/status", ""))
	if !strings.Contains(w.Body.String(), `"profile":{"label":"Garden"}`) {
		t.Errorf("Garden's status %s", w.Body)
	}
	plain := testServer(t)
	w = httptest.NewRecorder()
	plain.ServeHTTP(w, request(plain, "GET", "/api/status", ""))
	if strings.Contains(w.Body.String(), `"profile"`) {
		t.Errorf("a run of no profile has one: %s", w.Body)
	}
}

// A profile opens in this executable, with ui --profile and the name as
// an argument of its own, never through a shell, and with no file of
// this process's but the standard ones, whatever the name holds.
func TestProfileCommand(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Garden", "Back yard", `a "quoted" $(name); rm -rf x`, "--state-dir", "Straße"} {
		cmd, err := profileCommand(name)
		if err != nil {
			t.Fatal(err)
		}
		if cmd.Err != nil || cmd.Path != exe || !slices.Equal(cmd.Args, []string{exe, "ui", "--profile", name}) ||
			cmd.ExtraFiles != nil || cmd.Dir != "" || cmd.Env != nil || cmd.SysProcAttr != nil {
			t.Errorf("%q: path %q, args %q, extra files %v, err %v", name, cmd.Path, cmd.Args, cmd.ExtraFiles, cmd.Err)
		}
	}
}
