package ui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/filelock"
	"github.com/zmerlynn/seestar-wormhole/internal/icon"
	"github.com/zmerlynn/seestar-wormhole/internal/localhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// app is the app's one process: its local controls' server, which serves
// the shell page with the tab bar at / and each open profile's page under
// its tab's path, /p/<n>/, with that profile's own controller, settings,
// copier and lock.
type app struct {
	// root is the state root whose profiles open as tabs, or, when
	// profiles is false, the one folder --state-dir gave the run.
	root     string
	profiles bool
	host     string
	token    string
	addr     netip.AddrPort
	version  string
	logs     io.Writer
	run      runFunc
	chain    keychain
	native   *copyapp.Native
	window   bool
	// base is the tabs' parent context: it ends only after cleanup, so
	// the end of Run's context can't reach a proxy before its copier has
	// stopped.
	base context.Context
	// opening serializes opening tabs, so one profile can't open twice.
	opening sync.Mutex
	// ports orders its tabs' runs taking their local ports: each binds,
	// tells a conflict's holder and publishes what it serves before the
	// next binds.
	ports sync.Mutex
	// mu guards the tabs and each tab's closing, and orders a tab's
	// publishing against closing the last tab and quitting: once quitting
	// is set under it, no tab opens.
	mu   sync.Mutex
	tabs []*tab
	next int
	// pick is the tab an activation asked the shell to select, numbered
	// so the shell selects each ask once.
	pick     tabPick
	quit     chan struct{}
	quitOnce sync.Once
	quitting atomic.Bool
	activate func()
	// seen is when the shell last asked for the tabs, in Unix
	// nanoseconds: an activation reloads the window only when it isn't
	// live.
	seen atomic.Int64
}

// tab is an open profile: its folder, held by its ui.lock, and the
// server of its page.
type tab struct {
	app   *app
	name  string
	label string
	path  string
	dir   string
	st    *storage
	lock  *os.File
	// server serves the page, its API, copy's API and Browse, under path.
	server *server
	// closing is set, under the app's mu, when the tab starts to close;
	// the tab stays listed, its profile reserved, until closed is.
	closing bool
	closed  chan struct{}
}

type tabPick struct {
	Seq  int    `json:"seq"`
	Path string `json:"path"`
}

// tabKey carries the tab a run is of.
type tabKey struct{}

func tabOf(ctx context.Context) (*tab, bool) {
	t, ok := ctx.Value(tabKey{}).(*tab)
	return t, ok
}

// RunDir is the state folder of the UI run under ctx, the folder of its
// profile, or dir when the run isn't a tab's.
func RunDir(ctx context.Context, dir string) string {
	if t, ok := tabOf(ctx); ok {
		return t.dir
	}
	return dir
}

// PortGate holds the local ports of the app of the UI run under ctx,
// if the run is a tab's, until the func it returns is called: while the
// run binds its listeners, tells a conflict's holder and publishes what
// it serves, so another tab's run never finds a port held but not yet
// published.
func PortGate(ctx context.Context) func() {
	t, ok := tabOf(ctx)
	if !ok {
		return func() {}
	}
	t.app.ports.Lock()
	return t.app.ports.Unlock
}

// RunProfile is the profile of the UI run under ctx as the app shows it,
// "" for a run of a --state-dir folder or none.
func RunProfile(ctx context.Context) string {
	if t, ok := tabOf(ctx); ok && t.app.profiles {
		return t.label
	}
	return ""
}

// errLocked is a profile's folder another app holds.
var errLocked = errors.New("locked")

// lockedError is the refusal to open a profile another app holds, with
// the lock that says so.
func lockedError(label, dir string) error {
	path := filepath.Join(dir, "ui.lock")
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return errors.New(label + " is open in another seestar-wormhole window: its lock, " + path + ", is held")
}

func newApp(ctx, base context.Context, root string, profiles bool, logs io.Writer, run runFunc, chain keychain, native *copyapp.Native, window bool) (*app, net.Listener, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, nil, errors.New("Could not start the local controls.")
	}
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		listener.Close()
		return nil, nil, errors.New("Could not secure the local controls.")
	}
	listening := listener.Addr().(*net.TCPAddr).AddrPort()
	a := &app{root: root, profiles: profiles, host: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(listening.Port()))), token: hex.EncodeToString(token[:]),
		addr: netip.AddrPortFrom(listening.Addr().Unmap(), listening.Port()), version: uiVersion(ctx), logs: logs, run: run, chain: chain, native: native, window: window,
		base: base, quit: make(chan struct{})}
	return a, listener, nil
}

// record is the runtime record a tab's folder keeps while it is open.
func (a *app) record(path string) runtimeRecord {
	port, _ := strconv.Atoi(a.host[strings.LastIndexByte(a.host, ':')+1:])
	return runtimeRecord{Port: port, Token: a.token, Path: path}
}

// url is the shell page's private URL.
func (a *app) url() string { return a.record("").url() }

func (a *app) requestQuit() {
	a.quitting.Store(true)
	a.quitOnce.Do(func() { close(a.quit) })
}

// dir is the state folder of the profile name.
func (a *app) dir(name string) string {
	if !a.profiles {
		return a.root
	}
	return ProfileDir(a.root, name)
}

func (a *app) label(name string) string {
	if !a.profiles {
		return filepath.Base(a.root)
	}
	return ProfileLabel(name)
}

// find is the listed tab of the profile name, or the tab at path, which
// may be closing.
func (a *app) find(name, path string) *tab {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, t := range a.tabs {
		if path != "" && t.path == path || path == "" && sameProfile(t.name, name) {
			return t
		}
	}
	return nil
}

// open is the tabs that aren't closing.
func (a *app) open() []*tab {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(a.tabs), func(t *tab) bool { return t.closing })
}

// errQuitting is the refusal to open a tab once the app quits.
var errQuitting = errors.New("the app is quitting")

// openTab opens the profile name as a tab: it takes the folder's lock,
// loads its settings, starts its controller and copier, and records the
// tab in the folder's ui-runtime.json. A lock another app holds is
// errLocked. created, if set, is recorded in the tab's Activity.
func (a *app) openTab(name, created string) (_ *tab, result error) {
	if a.quitting.Load() {
		return nil, errQuitting
	}
	dir := a.dir(name)
	st, err := openStorage(dir)
	if err != nil {
		return nil, err
	}
	lock, err := privatefile.Open(filepath.Join(dir, "ui.lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		st.directory.Close()
		return nil, storageFailed(err)
	}
	acquired, err := filelock.TryLock(lock)
	if err != nil || !acquired {
		lock.Close()
		st.directory.Close()
		if err != nil {
			return nil, storageFailed(err)
		}
		return nil, errLocked
	}
	t := &tab{app: a, name: name, label: a.label(name), dir: dir, st: st, lock: lock, closed: make(chan struct{})}
	eventHistory := &eventLog{storage: st}
	eventHistory.add("App opened.")
	defer func() {
		if result != nil {
			eventHistory.add("App could not start.")
			lock.Close()
			st.directory.Close()
		}
	}()
	unsafeSettings := func(name string) error {
		settingsPath := filepath.Join(dir, name)
		if absolute, err := filepath.Abs(settingsPath); err == nil {
			settingsPath = absolute
		}
		return errors.New("Could not load saved settings safely.\n\n" + settingsPath + "\n\nCheck this file, then reopen seestar-wormhole.")
	}
	// The process's log names the profile of each record.
	logs := func(level slog.Level) *slog.Logger {
		logger := diagnostic.Logger("json", a.logs, level)
		if a.profiles {
			logger = logger.With("profile", t.label)
		}
		return logger
	}
	// A saved value a load replaced is logged, and the serial's value too.
	logFallbacks := func(fallbacks []diagnostic.Fallback) {
		diagnostic.LogFallbacks(logs(slog.LevelWarn), fallbacks)
	}
	settings, fallbacks, err := loadSettings(st)
	logFallbacks(fallbacks)
	routeSaved := err == nil && routeSavedIn(settings)
	if os.IsNotExist(err) {
		settings = defaultSettings()
	} else if err != nil {
		return nil, unsafeSettings("config.json")
	}
	// A copy.json this version can't read leaves Copy, Browse and Free
	// up space off for this tab and is never written; the proxy needs
	// none of it.
	copyFile, copySaved, fallbacks, err := loadCopySettings(st)
	logFallbacks(fallbacks)
	copyRefused := err != nil
	a.mu.Lock()
	a.next++
	t.path = "/p/" + strconv.Itoa(a.next) + "/"
	a.mu.Unlock()
	c := &controller{parent: context.WithValue(a.base, tabKey{}, t), run: a.run, text: runText, credentials: &credentialStore{chain: a.chain, savedEmail: func() string {
		if settings.Remember {
			return settings.Email
		}
		return ""
	}()}}
	handler := &server{version: a.version, host: a.host, token: a.token, controller: c, storage: st, settings: settings, quit: make(chan struct{})}
	handler.native = a.native != nil
	handler.routeSaved = routeSaved
	handler.copyRefused = copyRefused
	c.save = handler.saveSettings
	c.lastSaved = settings
	c.log = eventHistory
	t.server = handler
	if created != "" {
		eventHistory.add(created)
	}
	// The copier shares the file commands' default history folder, so the
	// two share one history and lock per folder. Its warnings and errors go
	// to the command's bounded log sink, as the proxy's do.
	if copyRefused {
		eventHistory.add(copyRefusedEvent)
	} else {
		copyState, _ := state.DefaultDir()
		handler.copy = newMount(a.base, copyMountOptions(handler, copyapp.MountOptions{
			Addr: a.addr, StateDir: copyState, Version: a.version,
			Log: logs(slog.LevelWarn), Settings: copySaved, Save: copyFile.save, Native: a.native,
		}))
	}
	if err = st.write("ui-runtime.json", a.record(t.path)); err != nil {
		quitCopy(handler)
		return nil, err
	}
	// A tab opened once the app quits would escape its shutdown.
	a.mu.Lock()
	if a.quitting.Load() {
		a.mu.Unlock()
		quitCopy(handler)
		os.Remove(filepath.Join(dir, "ui-runtime.json"))
		return nil, errQuitting
	}
	a.tabs = append(a.tabs, t)
	a.mu.Unlock()
	// Copying may start as the tab opens only once it is published.
	if handler.copy != nil {
		handler.copy.Start()
	}
	// The page's Quit quits the app.
	go func() {
		select {
		case <-handler.quit:
			a.requestQuit()
		case <-t.closed:
		}
	}()
	return t, nil
}

// close stops the tab's copier, then its run, and releases its folder,
// or waits for a close already under way to. Closing the last tab quits
// the app: no tab opens once it starts to close.
func (t *tab) close() {
	a := t.app
	a.mu.Lock()
	if t.closing {
		a.mu.Unlock()
		<-t.closed
		return
	}
	t.closing = true
	last := !slices.ContainsFunc(a.tabs, func(o *tab) bool { return !o.closing })
	if last {
		a.quitting.Store(true)
	}
	a.mu.Unlock()
	// Browse's sessions and the copier stop before the proxy they may be
	// reaching the telescope through.
	quitCopy(t.server)
	t.server.controller.quit()
	os.Remove(filepath.Join(t.dir, "ui-runtime.json"))
	os.Remove(filepath.Join(t.dir, bootstrapName))
	t.lock.Close()
	t.st.directory.Close()
	a.mu.Lock()
	a.tabs = slices.DeleteFunc(a.tabs, func(o *tab) bool { return o == t })
	a.mu.Unlock()
	close(t.closed)
	if last {
		a.requestQuit()
	}
}

// closeAll quits: no tab opens after it starts, an open already under
// way finishes first, and every tab closes, each tab's copier and run
// stopping as one closed alone does, closes under way included.
func (a *app) closeAll() {
	a.mu.Lock()
	a.quitting.Store(true)
	a.mu.Unlock()
	a.opening.Lock()
	a.opening.Unlock()
	a.mu.Lock()
	tabs := slices.Clone(a.tabs)
	a.mu.Unlock()
	var wg sync.WaitGroup
	for _, t := range tabs {
		wg.Go(t.close)
	}
	wg.Wait()
}

// holder is the tab other than self, closing or not, whose run listens
// on the local port at the bind address.
func (a *app) holder(self *tab, bind string, port uint16) (proxy.Holder, bool) {
	a.mu.Lock()
	tabs := slices.Clone(a.tabs)
	a.mu.Unlock()
	for _, t := range tabs {
		if t == self {
			continue
		}
		if status := t.server.serving(); status.serves(bind, port) {
			return proxy.Holder{Profile: t.label, Telescope: status.TelescopeName, Tab: true}, true
		}
	}
	return proxy.Holder{}, false
}

// busy is whether any tab is copying.
func (a *app) busy() bool {
	for _, t := range a.open() {
		if t.server.copy != nil && t.server.copy.Busy() {
			return true
		}
	}
	return false
}

// live is whether the shell asked for the tabs since mark, waiting up to
// wait for it to: a shell that is showing asks every second, and at once
// when its window is shown.
func (a *app) live(mark time.Time, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if a.seen.Load() >= mark.UnixNano() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// confirmQuit is whether quitting asks the shell to confirm first: some
// tab is copying and the shell asked for the tabs lately. A shell that
// doesn't answer never holds quitting up.
func (a *app) confirmQuit() bool {
	return !a.quitting.Load() && a.busy() && time.Since(time.Unix(0, a.seen.Load())) < 3*time.Second
}

// tabView is a tab as the shell shows it.
type tabView struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Label   string `json:"label"`
	Copying bool   `json:"copying"`
	Active  bool   `json:"active"`
}

// profileEntry is a profile the shell's + offers to open.
type profileEntry struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// tabsView answers the shell's poll: the open tabs in opening order,
// the profiles that exist but aren't open, whether + is offered, and the
// tab an activation asked for.
func (a *app) tabsView() (v struct {
	Tabs     []tabView      `json:"tabs"`
	Closed   []profileEntry `json:"closed"`
	Profiles bool           `json:"profiles"`
	Pick     tabPick        `json:"pick"`
	Error    string         `json:"error,omitempty"`
}) {
	tabs := a.open()
	v.Tabs, v.Closed, v.Profiles = []tabView{}, []profileEntry{}, a.profiles
	for _, t := range tabs {
		view := tabView{Path: t.path, Name: t.name, Label: t.label, Active: t.server.controller.view().Active}
		view.Copying = t.server.copy != nil && t.server.copy.Busy()
		v.Tabs = append(v.Tabs, view)
	}
	a.mu.Lock()
	v.Pick = a.pick
	a.mu.Unlock()
	if !a.profiles {
		return v
	}
	names, err := Profiles(a.root)
	if err != nil {
		v.Error = "Couldn't list the profiles: " + err.Error() + "."
	}
	for _, name := range append([]string{DefaultProfile}, names...) {
		if !slices.ContainsFunc(tabs, func(t *tab) bool { return sameProfile(t.name, name) }) {
			v.Closed = append(v.Closed, profileEntry{name, ProfileLabel(name)})
		}
	}
	return v
}

// tabInput is the body of the shell's actions and of an activation:
// the profile to open, by name, or the tab to close, by path.
type tabInput struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Profile string `json:"profile"`
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Each tab's page is a same-origin frame of the shell, and Browse one
	// of the page's, so a page may be framed only by its own origin.
	localhttp.SetHeaders(w.Header(), false)
	fail := func(code int) { http.Error(w, "Request rejected", code) }
	if r.Host != a.host {
		fail(http.StatusForbidden)
		return
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/p/"); ok {
		n, _, _ := strings.Cut(rest, "/")
		if t := a.find("", "/p/"+n+"/"); t != nil {
			http.StripPrefix("/p/"+n, t.server).ServeHTTP(w, r)
			return
		}
		fail(http.StatusNotFound)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		path := r.URL.Path
		if path == "/" {
			path = "/shell.html"
		}
		serveAsset(w, r, path, "/shell.html", "/shell.js", "/style.css", "/icon.svg")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Seestar-Token")), []byte(a.token)) != 1 {
		fail(http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/api/instance", "/api/tabs":
		if r.Method != "GET" {
			fail(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/api/tabs" {
			a.seen.Store(time.Now().UnixNano())
			localhttp.Respond(w, http.StatusOK, a.tabsView())
			return
		}
		// A relaunch shows this app's window, or opens its page when it
		// has none; one that names a profile asks an app with profile
		// tabs for that tab.
		if a.window {
			w.Header().Set("X-Seestar-Window", "1")
		}
		if a.profiles {
			w.Header().Set("X-Seestar-Tabs", "1")
		}
		localhttp.Respond(w, http.StatusOK, struct {
			Instance string `json:"instance"`
		}{"seestar-wormhole-ui-v1"})
		return
	}
	if r.Method != "POST" {
		fail(http.StatusMethodNotAllowed)
		return
	}
	if !allowedPost(w, r, a.host) {
		return
	}
	var in tabInput
	if err := json.UnmarshalRead(http.MaxBytesReader(w, r.Body, localhttp.MaxJSON), &in); err != nil {
		fail(http.StatusBadRequest)
		return
	}
	refuse := func(code, message string) {
		localhttp.Respond(w, http.StatusBadRequest, struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{code, message})
	}
	ok := func(v any) { localhttp.Respond(w, http.StatusOK, v) }
	switch r.URL.Path {
	case "/api/activate":
		if in.Profile != "" && !a.profiles {
			refuse("profile_open", "Couldn't open "+ProfileLabel(in.Profile)+": this seestar-wormhole window has one folder, "+a.root+", and no profiles.")
			return
		} else if in.Profile != "" {
			if err := a.openNamed(in.Profile); err != nil {
				refuse("profile_open", err.Error())
				return
			}
		}
		if a.activate != nil {
			a.activate()
		}
		ok(struct {
			OK bool `json:"ok"`
		}{true})
	case "/api/tabs/open", "/api/tabs/new":
		if !a.profiles {
			fail(http.StatusNotFound)
			return
		}
		name, created := in.Name, ""
		if r.URL.Path == "/api/tabs/new" {
			var err error
			name, err = CreateProfile(a.root, in.Name)
			if refused, isName := errors.AsType[*ProfileNameError](err); isName {
				refuse("profile_name", refused.Message)
				return
			} else if err != nil {
				refuse("profile_name", "Couldn't create the profile: "+err.Error()+".")
				return
			}
			created = "Created the profile " + name + "."
		}
		t, err := a.showCreated(name, created)
		if err != nil {
			refuse("profile_open", err.Error())
			return
		}
		ok(struct {
			Path string `json:"path"`
		}{t.path})
	case "/api/tabs/close":
		// The answer is the tabs left once it has closed, which the shell
		// shows: one opened meanwhile included, none if the app quits.
		if t := a.find("", in.Path); t != nil {
			t.close()
		}
		ok(a.tabsView())
	case "/api/quit":
		// Every tab's copier and run stop before the answer, as one
		// page's Quit stops its own.
		a.closeAll()
		ok(struct {
			OK bool `json:"ok"`
		}{true})
		if f, isFlusher := w.(http.Flusher); isFlusher {
			f.Flush()
		}
		a.requestQuit()
	default:
		fail(http.StatusNotFound)
	}
}

// openNamed opens the root's profile name as a tab, or selects it, and
// is the refusal's message when it can't.
func (a *app) openNamed(name string) error {
	_, err := a.showCreated(name, "")
	return err
}

// showCreated opens or selects the profile name, found as FindProfile
// finds it, recording created in its Activity if it opens now.
func (a *app) showCreated(name, created string) (*tab, error) {
	_, found, err := FindProfile(a.root, name)
	if err != nil {
		return nil, errors.New("Couldn't open " + ProfileLabel(name) + ": " + err.Error() + ".")
	}
	a.opening.Lock()
	t, err := a.openOrFind(found, created)
	a.opening.Unlock()
	if errors.Is(err, errLocked) {
		return nil, errors.New("Couldn't open " + ProfileLabel(found) + ": " + lockedError(ProfileLabel(found), a.dir(found)).Error() + ".")
	} else if err != nil {
		return nil, errors.New("Couldn't open " + ProfileLabel(found) + ": " + strings.TrimSuffix(err.Error(), ".") + ".")
	}
	a.mu.Lock()
	a.pick = tabPick{Seq: a.pick.Seq + 1, Path: t.path}
	a.mu.Unlock()
	return t, nil
}

// openOrFind is the open tab of the profile name, or the tab it opens:
// a tab of it that is closing finishes closing first. The caller holds
// opening.
func (a *app) openOrFind(name, created string) (*tab, error) {
	for {
		t := a.find(name, "")
		a.mu.Lock()
		quitting := a.quitting.Load()
		closing := t != nil && t.closing
		a.mu.Unlock()
		switch {
		case quitting:
			return nil, errQuitting
		case t == nil:
			return a.openTab(name, created)
		case !closing:
			return t, nil
		}
		<-t.closed
	}
}

// serveAsset serves one of the embedded web assets named.
func serveAsset(w http.ResponseWriter, r *http.Request, path string, names ...string) {
	fail := func(code int) { http.Error(w, "Request rejected", code) }
	if r.Method != "GET" && r.Method != "HEAD" {
		fail(http.StatusMethodNotAllowed)
		return
	}
	if !slices.Contains(names, path) {
		fail(http.StatusNotFound)
		return
	}
	b, err := assets.ReadFile("web" + path)
	if path == "/icon.svg" {
		b, err = icon.SVG, nil
	}
	if err != nil {
		fail(http.StatusNotFound)
		return
	}
	contentType := "text/html; charset=utf-8"
	switch filepath.Ext(path) {
	case ".js":
		contentType = "text/javascript; charset=utf-8"
	case ".css":
		contentType = "text/css; charset=utf-8"
	case ".svg":
		contentType = "image/svg+xml"
	}
	w.Header().Set("Content-Type", contentType)
	if r.Method != "HEAD" {
		w.Write(b)
	}
}
