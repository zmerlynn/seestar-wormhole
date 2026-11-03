package app

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/browse"
	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

//go:embed web
var assets embed.FS

// The app page's policy is browse's, plus framing the Browse tab, which is
// browse served from this same origin.
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; " +
	"frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// tokenHeader carries the token from the page's URL fragment on every API
// call.
const tokenHeader = "X-Seestar-Copy-Token"

// maxJSON caps a POST body.
const maxJSON = 64 << 10

// testTimeout bounds one Test.
const testTimeout = 30 * time.Second

var static = map[string]struct{ file, typ string }{
	"/app/":          {"web/index.html", "text/html; charset=utf-8"},
	"/app/app.js":    {"web/app.js", "text/javascript; charset=utf-8"},
	"/app/style.css": {"web/style.css", "text/css; charset=utf-8"},
	"/app/icon.svg":  {"web/icon.svg", "image/svg+xml"},
}

// server is the app's handler. /app/ is the app's page and API; every
// other path goes to the current browse handler, which a save of a new
// address or folder replaces.
type server struct {
	ctx      context.Context // the app's lifetime; browse's sessions live in it
	addr     netip.AddrPort  // the listener's
	token    string
	version  string
	stateDir string
	log      *slog.Logger
	// dial opens a browse session; test runs Test. Tests replace both.
	dial   func(ctx context.Context, endpoint string) (remote.Session, error)
	test   func(ctx context.Context, endpoint string) ([]remote.Entry, error)
	native *Native // nil without a window
	copy   *controller
	free   *freer

	mu       sync.Mutex
	file     *settingsFile
	browse   *browse.Handler // nil until an address and a folder are saved
	sessions *browse.Sessions
}

func newServer(ctx context.Context, addr netip.AddrPort, token, version, stateDir string, file *settingsFile, log *slog.Logger) *server {
	s := &server{
		ctx: ctx, addr: addr, token: token, version: version, stateDir: stateDir, file: file, log: log,
		dial: func(ctx context.Context, endpoint string) (remote.Session, error) { return remote.Dial(ctx, endpoint) },
		test: engine.Test,
	}
	s.copy = newController(ctx, stateDir, log, func(ctx context.Context, endpoint string) (remote.Session, error) {
		return s.dial(ctx, endpoint)
	})
	s.free = newFreer(ctx, stateDir, log, func(ctx context.Context, endpoint string) (remote.Session, error) {
		return s.dial(ctx, endpoint)
	})
	return s
}

// setUp reports whether an address and a folder are saved. Until they
// are, only Settings is open.
func (f *settingsFile) setUp() bool {
	return f.exists && f.settings.Address != "" && f.settings.Folder != ""
}

// startBrowse serves Browse for the saved settings, if they are set up. It
// is called once the server's fields are set and before it serves.
func (s *server) startBrowse() {
	if s.file.setUp() {
		s.browse, s.sessions = s.newBrowse(s.file.settings)
	}
}

// newBrowse builds a browse handler with its own session pool for the
// address and records for the folder.
func (s *server) newBrowse(set Settings) (*browse.Handler, *browse.Sessions) {
	endpoint, folder := set.Address, set.Folder
	log := s.log.With("endpoint", endpoint)
	sessions := browse.NewSessions(s.ctx, func(ctx context.Context) (remote.Session, error) {
		return s.dial(ctx, endpoint)
	}, log)
	records := func() (map[string]state.Record, error) { return state.Load(s.stateDir, folder, log) }
	if file, err := state.Path(s.stateDir, folder); err == nil {
		records = browse.CacheRecords(file, records)
	}
	h := browse.NewHandler(browse.Config{
		Addr: s.addr, Endpoint: endpoint, Sessions: sessions, Records: records, Log: log, InApp: true,
	})
	return h, sessions
}

// startCopying turns copying on with the saved settings. Holding s.mu
// keeps a Save from landing between the check and the start.
func (s *server) startCopying() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.file.setUp() {
		return errNotSetUp
	}
	return s.copy.start(s.file.settings)
}

var errNotSetUp = errors.New("set the telescope and folder in Settings first")

// close ends the current browse pool.
func (s *server) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions != nil {
		s.sessions.Close()
	}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/app/") {
		s.mu.Lock()
		h, sessions := s.browse, s.sessions
		s.mu.Unlock()
		if h == nil {
			setHeaders(w)
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		// A window's webview may not save a download itself, so the app
		// does, with a native save dialog.
		if s.native == nil {
			h.ServeHTTP(w, r)
		} else if p, ok := h.Download(w, r); ok {
			s.saveFile(w, r, sessions, p)
		}
		return
	}
	setHeaders(w)
	fail := func(code int) { http.Error(w, "Request rejected", code) }
	if r.Host != s.addr.String() {
		fail(http.StatusForbidden)
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		fail(http.StatusForbidden)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/app/api/") {
		asset, ok := static[r.URL.Path]
		if !ok {
			fail(http.StatusNotFound)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			fail(http.StatusMethodNotAllowed)
			return
		}
		b, err := assets.ReadFile(asset.file)
		if err != nil {
			fail(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", asset.typ)
		if r.Method == "GET" {
			w.Write(b)
		}
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), []byte(s.token)) != 1 {
		fail(http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/app/api/status":
		if r.Method != "GET" {
			fail(http.StatusMethodNotAllowed)
			return
		}
		s.status(w)
		return
	case "/app/api/test", "/app/api/settings", "/app/api/copy", "/app/api/delete", "/app/api/clear":
	case "/app/api/folder":
		if s.native == nil {
			fail(http.StatusNotFound)
			return
		}
	default:
		fail(http.StatusNotFound)
		return
	}
	if r.Method != "POST" {
		fail(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "http://"+s.addr.String() || r.Header.Get("Sec-Fetch-Site") == "none" {
		fail(http.StatusForbidden)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(http.StatusUnsupportedMediaType)
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSON))
	if err != nil {
		fail(http.StatusRequestEntityTooLarge)
		return
	}
	var in input
	if err := json.Unmarshal(b, &in); err != nil {
		fail(http.StatusBadRequest)
		return
	}
	switch r.URL.Path {
	case "/app/api/test":
		s.runTest(w, r.Context(), in)
	case "/app/api/folder":
		s.chooseFolder(w, in)
	case "/app/api/copy":
		s.copyAction(w, in.Action)
	case "/app/api/delete":
		s.freeAction(w, in)
	case "/app/api/clear":
		s.clearHistory(w)
	default:
		s.save(w, in)
	}
}

func setHeaders(w http.ResponseWriter) {
	hd := w.Header()
	hd.Set("Content-Security-Policy", csp)
	hd.Set("X-Frame-Options", "DENY")
	hd.Set("Cross-Origin-Resource-Policy", "same-origin")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", "no-store")
}

// input is what the page posts: the form's fields as typed. Test reads
// only the address.
type input struct {
	Address      string   `json:"address"`
	Folder       string   `json:"folder"`
	Interval     string   `json:"interval"`
	Excludes     []string `json:"excludes"` // the box's lines
	Includes     []string `json:"includes"`
	SkipBuiltIn  bool     `json:"skip_built_in"`
	StartCopying bool     `json:"start_copying"`
	Action       string   `json:"action"` // the copy and delete APIs'
	// The delete API's: Find's options, and Delete's preview and folders.
	Days    string   `json:"days"`
	Trust   bool     `json:"trust"`
	ID      int      `json:"id"`
	Folders []string `json:"folders"`
}

func (s *server) status(w http.ResponseWriter) {
	s.mu.Lock()
	set, setUp, file := s.file.settings, s.file.setUp(), s.file.path
	s.mu.Unlock()
	// The saved folder's state file, as sync names it.
	var history string
	if setUp {
		history, _ = state.Path(s.stateDir, set.Folder)
	}
	lines, seq := s.copy.activity.lines()
	respond(w, http.StatusOK, struct {
		Version         string   `json:"version"`
		SetUp           bool     `json:"set_up"`
		Settings        Settings `json:"settings"`
		SettingsFile    string   `json:"settings_file"`
		History         string   `json:"history"` // "" until set up
		DefaultExcludes []string `json:"default_excludes"`
		Native          bool     `json:"native"` // the window's dialogs are there
		Copy            copyView `json:"copy"`
		Free            freeView `json:"free"`
		Activity        []entry  `json:"activity"` // newest first
		ActivitySeq     int      `json:"activity_seq"`
	}{s.version, setUp, set, file, history, engine.DefaultExcludes, s.native != nil, s.copy.view(time.Now()), s.free.view(), lines, seq})
}

func (s *server) runTest(w http.ResponseWriter, ctx context.Context, in input) {
	endpoint, ok := normalizeAddress(in.Address)
	if !ok {
		respond(w, http.StatusBadRequest, fieldErrors{map[string]string{"address": addressError}})
		return
	}
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	entries, err := s.test(ctx, endpoint)
	if err != nil {
		s.log.Warn("test failed", "endpoint", endpoint, "err", err)
	} else {
		s.log.Info("test passed", "endpoint", endpoint)
	}
	respond(w, http.StatusOK, struct {
		OK      bool   `json:"ok"`
		Address string `json:"address"`
		Message string `json:"message"`
	}{err == nil, endpoint, testMessage(endpoint, entries, err)})
}

type fieldErrors struct {
	Fields map[string]string `json:"fields"`
}

func (s *server) save(w http.ResponseWriter, in input) {
	set, errs := checkInput(in)
	if len(errs) > 0 {
		respond(w, http.StatusBadRequest, fieldErrors{errs})
		return
	}
	s.mu.Lock()
	if s.copy.on() {
		s.mu.Unlock()
		respond(w, http.StatusConflict, struct {
			Error string `json:"error"`
		}{"Stop copying to change settings."})
		return
	}
	old, wasSetUp := s.file.settings, s.file.setUp()
	// The Free up space options are saved by Find, not by this form.
	set.OlderThanDays, set.TrustState = old.OlderThanDays, old.TrustState
	if err := s.file.save(set); err != nil {
		s.mu.Unlock()
		s.log.Error("save settings", "path", s.file.path, "err", err)
		// The page shows the reason without the temp file's name.
		respond(w, http.StatusInternalServerError, struct {
			Error string `json:"error"`
		}{"Couldn't save settings: " + reason(err) + ". Your changes have not been saved. Try saving again."})
		return
	}
	changed := !wasSetUp || old.Address != set.Address || old.Folder != set.Folder
	if changed || !slices.Equal(old.Excludes, set.Excludes) || !slices.Equal(old.Includes, set.Includes) || old.SkipBuiltIn != set.SkipBuiltIn {
		s.free.clear()
	}
	var oldSessions *browse.Sessions
	if changed {
		oldSessions = s.sessions
		s.browse, s.sessions = s.newBrowse(set)
	}
	s.mu.Unlock()
	s.log.Info("settings saved", "path", s.file.path)
	// A request still on the old pool fails; the page reloads the frame.
	if oldSessions != nil {
		oldSessions.Close()
	}
	respond(w, http.StatusOK, struct {
		Settings      Settings `json:"settings"`
		BrowseChanged bool     `json:"browse_changed"`
	}{set, changed})
}

// copyAction starts or stops copying, or starts the next pass now, and
// answers the Copy tab's new state.
func (s *server) copyAction(w http.ResponseWriter, action string) {
	var err error
	switch action {
	case "start":
		err = s.startCopying()
	case "stop":
		s.copy.stop()
	case "check":
		s.copy.check()
	default:
		http.Error(w, "Request rejected", http.StatusBadRequest)
		return
	}
	if err != nil {
		respond(w, http.StatusConflict, struct {
			Error string `json:"error"`
		}{"Couldn't start copying: " + err.Error() + "."})
		return
	}
	respond(w, http.StatusOK, s.copy.view(time.Now()))
}

// freeAction is the Free up space tab's: find, cancel, delete, stop or
// reset. It answers the tab's new state.
func (s *server) freeAction(w http.ResponseWriter, in input) {
	conflict := func(msg string) {
		respond(w, http.StatusConflict, struct {
			Error string `json:"error"`
		}{msg})
	}
	switch in.Action {
	case "find":
		days, err := strconv.Atoi(strings.TrimSpace(in.Days))
		if err != nil || days < 0 || days > maxDays {
			respond(w, http.StatusBadRequest, fieldErrors{map[string]string{"days": "Enter a whole number of days, 0 or more."}})
			return
		}
		// Holding s.mu keeps a Save from landing between the read and
		// the start; it is released before logging or answering.
		s.mu.Lock()
		if !s.file.setUp() {
			s.mu.Unlock()
			conflict("Couldn't preview: " + errNotSetUp.Error() + ".")
			return
		}
		set, old := s.file.settings, s.file.settings
		set.OlderThanDays, set.TrustState = days, in.Trust
		if set.OlderThanDays != old.OlderThanDays || set.TrustState != old.TrustState {
			if err := s.file.save(set); err != nil {
				s.mu.Unlock()
				s.log.Error("save settings", "path", s.file.path, "err", err)
				conflict("Couldn't save the options: " + reason(err) + ". Try again.")
				return
			}
		}
		err = s.free.find(set)
		s.mu.Unlock()
		if err != nil {
			conflict("Couldn't preview: " + err.Error() + ".")
			return
		}
	case "cancel":
		s.free.cancelCheck()
	case "delete":
		if err := s.free.confirm(in.ID, in.Folders); errors.Is(err, errStale) {
			conflict("Preview again before deleting.")
			return
		} else if err != nil {
			conflict("Couldn't delete: " + err.Error() + ".")
			return
		}
	case "stop":
		s.free.halt()
	case "reset":
		s.free.reset()
	default:
		http.Error(w, "Request rejected", http.StatusBadRequest)
		return
	}
	respond(w, http.StatusOK, s.free.view())
}

// clearHistory deletes the saved folder's state file, as a reset of sync
// does, so the next pass adopts the files still in the folder and copies
// the rest again. It refuses while copying is on, while a Free up space
// check or delete runs, and while another copy holds the folder's lock.
// A Free up space preview goes back to Start, since its proof is gone.
func (s *server) clearHistory(w http.ResponseWriter) {
	readonly.Check("clear history")
	conflict := func(msg string) {
		respond(w, http.StatusConflict, struct {
			Error string `json:"error"`
		}{msg})
	}
	s.mu.Lock()
	folder := s.file.settings.Folder
	var err error
	switch {
	case !s.file.setUp():
		err = errNotSetUp
	case s.copy.on():
		err = errors.New("stop copying first")
	case !s.free.forget():
		err = errors.New("wait for Free up space to finish")
	default:
		err = state.Remove(s.stateDir, folder)
	}
	s.mu.Unlock()
	switch {
	case errors.Is(err, state.ErrLocked):
		conflict("Couldn't clear the history: another copy is using this folder, in another seestar-copy window or seestar-copy-cli. Stop that copy, then try again.")
	case err != nil:
		s.log.Warn("clear history", "dest", folder, "err", err)
		conflict("Couldn't clear the history: " + reason(err) + ".")
	default:
		s.log.Info("copy history cleared", "dest", folder)
		respond(w, http.StatusOK, struct{}{})
	}
}

// maxDays caps Only files older than N days: 100 years.
const maxDays = 36500

func respond(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "Response unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(b)
}

// reason is err's text without the path a file error names.
func reason(err error) string {
	var pe *fs.PathError
	var le *os.LinkError
	if errors.As(err, &pe) {
		err = pe.Err
	} else if errors.As(err, &le) {
		err = le.Err
	}
	return err.Error()
}

// chooseFolder shows the folder picker, starting at the typed folder or
// the nearest folder above it that exists, and answers the folder chosen,
// or "" on Cancel.
func (s *server) chooseFolder(w http.ResponseWriter, in input) {
	dir := strings.TrimSpace(in.Folder)
	for filepath.IsAbs(dir) {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			break
		}
		up := filepath.Dir(dir)
		if up == dir {
			dir = ""
		} else {
			dir = up
		}
	}
	if !filepath.IsAbs(dir) {
		dir = ""
	}
	chosen, err := s.native.ChooseFolder(dir)
	if err != nil {
		s.log.Error("folder picker", "err", err)
		respond(w, http.StatusInternalServerError, struct {
			Error string `json:"error"`
		}{"Couldn't open the folder picker: " + err.Error() + ". Type the folder's full path instead."})
		return
	}
	respond(w, http.StatusOK, struct {
		Folder string `json:"folder"`
	}{chosen})
}

// saveFile asks where to save the device's file p and saves it there. It
// answers 204 in every case, so the page stays where it is; a failed save
// is shown in a native message, unless the app is stopping.
func (s *server) saveFile(w http.ResponseWriter, r *http.Request, sessions *browse.Sessions, p string) {
	name := path.Base(p)
	dst, err := s.native.SaveFile(name)
	if err == nil && dst != "" {
		err = browse.Save(r.Context(), sessions, p, dst)
		if err == nil {
			s.log.Info("file saved", "path", p, "to", dst)
		}
	}
	if err != nil && r.Context().Err() == nil {
		s.log.Warn("save failed", "path", p, "to", dst, "err", err)
		s.native.Alert("Couldn't save " + name + ": " + reason(err) + ". Try saving it again.")
	}
	w.WriteHeader(http.StatusNoContent)
}
