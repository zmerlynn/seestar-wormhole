package app

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"log/slog"
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

	"github.com/zmerlynn/seestar-wormhole/internal/copy/browse"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/engine"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/localhttp"
)

// testTimeout bounds one Test.
const testTimeout = 30 * time.Second

// server is a [Mount]'s handler: copy's API under /copy/api/, and Browse
// under /browse/, which a save of a new folder or selection replaces.
//
// Locks are taken in this order: admit, then mu, then a controller's own
// mutex. A [Mount]'s host takes its own locks inside admit, in its commit,
// and target takes the host's status lock inside mu, so neither of those
// is held around admit.
type server struct {
	ctx      context.Context // the app's lifetime; browse's sessions live in it
	addr     netip.AddrPort  // the listener's
	version  string
	stateDir string
	log      *slog.Logger
	// dial opens a browse session at the target; test runs Test, and
	// reach a route's Save's check.
	dial  func(ctx context.Context) (remote.Session, error)
	test  func(ctx context.Context, endpoint string) ([]remote.Entry, error)
	reach func(ctx context.Context, endpoint string) error
	// target is where the telescope is for every dial and start.
	target  func() (Target, error)
	native  Native // the window's dialogs; each nil without one
	copy    *controller
	preview *previewer
	free    *freer

	// admit is held by each change of where the telescope is or what is
	// saved, and by each start of copying, a pass of copying, a preview,
	// a Free up space check or a delete, so none lands between another's
	// checks and its start. It may be held across a save to disk but is
	// never taken by status, views, Busy or the end of any of them.
	admit sync.Mutex

	mu       sync.Mutex
	file     *settingsFile
	browse   *browse.Handler // nil until a folder is saved
	sessions *browse.Sessions
}

func newServer(ctx context.Context, o MountOptions, file *settingsFile) *server {
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &server{
		ctx: ctx, addr: o.Addr, version: o.Version, stateDir: o.StateDir, target: o.Target, file: file, log: log,
		test: func(ctx context.Context, endpoint string) ([]remote.Entry, error) {
			return engine.Test(ctx, endpoint, log)
		},
	}
	if o.Test != nil {
		s.test = o.Test
	}
	s.reach = o.Reach
	if s.reach == nil {
		s.reach = remote.Reach
	}
	if o.Native != nil {
		s.native = *o.Native
	}
	dial := o.Dial
	if dial == nil {
		dial = func(ctx context.Context, endpoint string) (remote.Session, error) {
			return remote.Dial(ctx, endpoint, log)
		}
	}
	// Browse's dials ask for the endpoint again, so they reach the
	// telescope where it is now. A change of route replaces Browse's pool.
	s.dial = func(ctx context.Context) (remote.Session, error) {
		t, err := s.target()
		if err != nil {
			return nil, err
		}
		return dial(ctx, t.Endpoint)
	}
	// Each pass of copying follows its telescope to where it is now, and
	// copying stops once the target is another telescope.
	s.copy = newController(ctx, o.StateDir, log, dial)
	s.copy.admit, s.copy.target = &s.admit, s.target
	// A preview's and a delete's dials go only to the target they were
	// started at, and fail once that changed, even by a reconnect: a
	// delete must not remove files on a telescope it never checked.
	at := func(t Target) error {
		now, err := s.target()
		if err == nil && now != t {
			err = errMoved
		}
		return err
	}
	s.preview = newPreviewer(ctx, o.StateDir, log, dial)
	s.preview.at = at
	s.free = newFreer(ctx, o.StateDir, log, dial)
	s.free.at = at
	// Free up space's outcomes join copy's in Activity, tagged "delete".
	s.free.record = slog.New(newTee(slog.DiscardHandler, s.copy.activity, "delete"))
	if o.Remover != nil {
		s.free.remover = o.Remover
	}
	if s.setUp() {
		s.browse, s.sessions = s.newBrowse(file.settings)
	}
	return s
}

// setUp reports whether the saved settings are enough to copy: a folder,
// since the address is the target's.
func (s *server) setUp() bool {
	return s.file.exists && s.file.settings.Folder != ""
}

// resolve is set with the target's address now, and the target.
func (s *server) resolve(set Settings) (Settings, Target, error) {
	t, err := s.target()
	set.Address = t.Endpoint
	return set, t, err
}

// newBrowse builds a browse handler with its own session pool, which
// reaches the target, records for the folder, and what the selection
// leaves out.
func (s *server) newBrowse(set Settings) (*browse.Handler, *browse.Sessions) {
	folder, log := set.Folder, s.log
	sel, err := selector(set)
	if err != nil {
		log.Warn("browse shows nothing as left out", "err", err)
	}
	sessions := browse.NewSessions(s.ctx, s.dial, log)
	records := func() (map[string]state.Record, error) { return state.Load(s.stateDir, folder, log) }
	if file, err := state.Path(s.stateDir, folder); err == nil {
		records = browse.CacheRecords(file, records)
	}
	h := browse.NewHandler(browse.Config{
		Addr: s.addr, Sessions: sessions, Records: records, Selector: sel, Log: log, InApp: true,
	})
	return h, sessions
}

// startCopying turns copying on with the saved settings, which discards
// a preview. Holding s.admit keeps a Save or a change of endpoint from
// landing between the check and the start.
func (s *server) startCopying() error {
	s.admit.Lock()
	defer s.admit.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.setUp() {
		return errNotSetUp
	}
	set, t, err := s.resolve(s.file.settings)
	if err != nil {
		return err
	}
	if err := s.copy.start(set, t); err != nil {
		return err
	}
	s.preview.discard()
	return nil
}

// startPreview runs a preview with the saved settings while copying is
// off. Holding s.admit keeps a Save, a Start or a change of endpoint from
// landing between the checks and the start.
func (s *server) startPreview() error {
	s.admit.Lock()
	defer s.admit.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.setUp():
		return errNotSetUp
	case s.copy.on():
		return errors.New("copying is on")
	}
	set, t, err := s.resolve(s.file.settings)
	if err != nil {
		return err
	}
	return s.preview.start(set, t)
}

// copyView is the Copy tab with its preview.
func (s *server) copyView() copyView {
	v := s.copy.view(time.Now())
	v.Preview = s.preview.view()
	return v
}

var errNotSetUp = errors.New("save the copy settings first")

// endpointChanged follows a change of where the telescope is, which the
// caller made holding s.admit: the previews go back to Start, as their
// proof was taken elsewhere, and browse gets a new pool, so no request
// reaches the old endpoint through a session already open there.
func (s *server) endpointChanged() {
	s.mu.Lock()
	s.free.clear()
	s.preview.discard()
	old := s.sessions
	if s.browse != nil {
		s.browse, s.sessions = s.newBrowse(s.file.settings)
	}
	s.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// close ends the current browse pool.
func (s *server) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions != nil {
		s.sessions.Close()
	}
}

// ServeHTTP serves copy's API under /copy/api/ and Browse under /browse/.
// The caller has checked the Host, the token on an API call, and a POST's
// Origin, Fetch Metadata and content type; Browse makes its own checks,
// since its frame sends no token.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/copy/api/"):
		s.api(w, r, strings.TrimPrefix(r.URL.Path, "/copy/api/"))
	case strings.HasPrefix(r.URL.Path, "/browse/"):
		s.mu.Lock()
		h, sessions := s.browse, s.sessions
		s.mu.Unlock()
		if h == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		// A window's webview may not save a download itself, so the app
		// does, with the window's save dialog.
		var serve http.Handler = h
		if s.native.SaveFile != nil {
			serve = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if p, ok := h.Download(w, r); ok {
					s.saveFile(w, r, sessions, p)
				}
			})
		}
		http.StripPrefix("/browse", serve).ServeHTTP(w, r)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

// api serves the API call name, which is the path after the API's prefix.
func (s *server) api(w http.ResponseWriter, r *http.Request, name string) {
	fail := func(code int) { http.Error(w, "Request rejected", code) }
	switch name {
	case "status":
		if r.Method != "GET" {
			fail(http.StatusMethodNotAllowed)
			return
		}
		s.status(w)
		return
	case "test", "settings", "copy", "delete", "clear":
	case "folder":
		if s.native.ChooseFolder == nil {
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
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, localhttp.MaxJSON))
	if err != nil {
		fail(http.StatusRequestEntityTooLarge)
		return
	}
	var in input
	if err := json.Unmarshal(b, &in); err != nil {
		fail(http.StatusBadRequest)
		return
	}
	switch name {
	case "test":
		s.runTest(w, r.Context(), in)
	case "folder":
		s.chooseFolder(w, in)
	case "copy":
		s.copyAction(w, in.Action)
	case "delete":
		s.freeAction(w, in)
	case "clear":
		s.clearHistory(w)
	default:
		s.save(w, in)
	}
}

// input is what the page posts: the form's fields as typed. Test reads
// only the address.
type input struct {
	Address      string   `json:"address"`
	Folder       string   `json:"folder"`
	Interval     string   `json:"interval"`
	FilesAtOnce  string   `json:"files_at_once"`
	Excludes     []string `json:"excludes"` // the box's lines
	Includes     []string `json:"includes"`
	SkipBuiltIn  bool     `json:"skip_built_in"`
	StartCopying bool     `json:"start_copying"`
	Once         bool     `json:"once"`
	Action       string   `json:"action"` // the copy and delete APIs'
	// The delete API's: Find's options, and Delete's preview and folders.
	Days    string   `json:"days"`
	Trust   bool     `json:"trust"`
	ID      int      `json:"id"`
	Folders []string `json:"folders"`
}

func (s *server) status(w http.ResponseWriter) {
	s.mu.Lock()
	set, setUp := s.file.settings, s.setUp()
	s.mu.Unlock()
	// The saved folder's state file, as sync names it.
	var history string
	if setUp {
		history, _ = state.Path(s.stateDir, set.Folder)
	}
	lines, seq := s.copy.activity.lines()
	localhttp.Respond(w, http.StatusOK, struct {
		Version         string   `json:"version"`
		SetUp           bool     `json:"set_up"`
		Settings        Settings `json:"settings"`
		History         string   `json:"history"` // "" until set up
		DefaultExcludes []string `json:"default_excludes"`
		Copy            copyView `json:"copy"`
		Free            freeView `json:"free"`
		Activity        []entry  `json:"activity"` // newest first
		ActivitySeq     int      `json:"activity_seq"`
		Choose          bool     `json:"choose"` // the window's folder picker is there
	}{s.version, setUp, set, history, engine.DefaultExcludes, s.copyView(), s.free.view(), lines, seq, s.native.ChooseFolder != nil})
}

func (s *server) runTest(w http.ResponseWriter, ctx context.Context, in input) {
	endpoint, ok := normalizeAddress(in.Address)
	if !ok {
		localhttp.Respond(w, http.StatusBadRequest, fieldErrors{map[string]string{"address": addressError}})
		return
	}
	entries, err := s.testLAN(ctx, endpoint)
	if err != nil {
		s.log.Warn("test failed", "endpoint", endpoint, "err", err)
	} else {
		s.log.Info("test passed", "endpoint", endpoint)
	}
	localhttp.Respond(w, http.StatusOK, struct {
		OK      bool   `json:"ok"`
		Address string `json:"address"`
		Message string `json:"message"`
	}{err == nil, endpoint, testMessage(hostOS, endpoint, entries, err)})
}

// testLAN runs Test of endpoint, an address on this network, within
// testTimeout, waiting out macOS's Local Network prompt.
func (s *server) testLAN(ctx context.Context, endpoint string) ([]remote.Entry, error) {
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	return retryLAN(ctx, localNetworkRetry, func(ctx context.Context) ([]remote.Entry, error) { return s.test(ctx, endpoint) })
}

// reachLAN is testLAN for a route's Save: it checks only that the
// telescope answers at endpoint, since the page's Test has checked its
// share.
func (s *server) reachLAN(ctx context.Context, endpoint string) error {
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	_, err := retryLAN(ctx, localNetworkRetry, func(ctx context.Context) (struct{}, error) { return struct{}{}, s.reach(ctx, endpoint) })
	return err
}

type fieldErrors struct {
	Fields map[string]string `json:"fields"`
}

func (s *server) save(w http.ResponseWriter, in input) {
	set, errs := checkInput(in)
	if len(errs) > 0 {
		localhttp.Respond(w, http.StatusBadRequest, fieldErrors{errs})
		return
	}
	s.admit.Lock()
	defer s.admit.Unlock()
	s.mu.Lock()
	if s.copy.on() {
		s.mu.Unlock()
		localhttp.Respond(w, http.StatusConflict, struct {
			Error string `json:"error"`
		}{"Stop copying to change settings."})
		return
	}
	old, wasSetUp := s.file.settings, s.setUp()
	// The Free up space options are saved by Find, not by this form.
	set.OlderThanDays, set.TrustState = old.OlderThanDays, old.TrustState
	if err := s.file.save(set); err != nil {
		s.mu.Unlock()
		s.log.Error("save settings", "err", err)
		// The page shows the reason without the temp file's name.
		localhttp.Respond(w, http.StatusInternalServerError, struct {
			Error string `json:"error"`
		}{"Couldn't save settings: " + reason(err) + ". Your changes have not been saved. Try saving again."})
		return
	}
	// A new selection replaces browse too, which shows what it leaves out.
	changed := !wasSetUp || old.Folder != set.Folder ||
		!slices.Equal(old.Excludes, set.Excludes) || !slices.Equal(old.Includes, set.Includes) || old.SkipBuiltIn != set.SkipBuiltIn
	var oldSessions *browse.Sessions
	if changed {
		s.free.clear()
		s.preview.discard()
		oldSessions = s.sessions
		s.browse, s.sessions = s.newBrowse(set)
	}
	s.mu.Unlock()
	s.log.Info("settings saved")
	// A request still on the old pool fails; the page reloads the frame.
	if oldSessions != nil {
		oldSessions.Close()
	}
	localhttp.Respond(w, http.StatusOK, struct {
		Settings      Settings `json:"settings"`
		BrowseChanged bool     `json:"browse_changed"`
	}{set, changed})
}

// copyAction starts or stops copying, starts the next pass now, or starts
// or cancels a preview, and answers the Copy tab's new state.
func (s *server) copyAction(w http.ResponseWriter, action string) {
	var err error
	verb := "start copying"
	switch action {
	case "start":
		err = s.startCopying()
	case "stop":
		s.copy.stop()
	case "check":
		s.copy.check()
	case "preview":
		err, verb = s.startPreview(), "preview"
	case "cancel":
		s.preview.discard()
	default:
		http.Error(w, "Request rejected", http.StatusBadRequest)
		return
	}
	if err != nil {
		localhttp.Respond(w, http.StatusConflict, struct {
			Error string `json:"error"`
		}{"Couldn't " + verb + ": " + err.Error() + "."})
		return
	}
	localhttp.Respond(w, http.StatusOK, s.copyView())
}

// freeAction is the Free up space tab's: find, cancel, delete, stop or
// reset. It answers the tab's new state.
func (s *server) freeAction(w http.ResponseWriter, in input) {
	conflict := func(msg string) {
		localhttp.Respond(w, http.StatusConflict, struct {
			Error string `json:"error"`
		}{msg})
	}
	switch in.Action {
	case "find":
		days, err := strconv.Atoi(strings.TrimSpace(in.Days))
		if err != nil || days < 0 || days > maxDays {
			localhttp.Respond(w, http.StatusBadRequest, fieldErrors{map[string]string{"days": "Enter a whole number of days, 0 or more."}})
			return
		}
		// Holding s.admit keeps a Save or a change of endpoint from
		// landing between the read and the start; s.mu is released
		// before logging or answering.
		s.admit.Lock()
		defer s.admit.Unlock()
		s.mu.Lock()
		if !s.setUp() {
			s.mu.Unlock()
			conflict("Couldn't preview: " + errNotSetUp.Error() + ".")
			return
		}
		set, old := s.file.settings, s.file.settings
		set.OlderThanDays, set.TrustState = days, in.Trust
		if set.OlderThanDays != old.OlderThanDays || set.TrustState != old.TrustState {
			if err := s.file.save(set); err != nil {
				s.mu.Unlock()
				s.log.Error("save settings", "err", err)
				conflict("Couldn't save the options: " + reason(err) + ". Try again.")
				return
			}
		}
		var t Target
		if set, t, err = s.resolve(set); err == nil {
			err = s.free.find(set, t)
		}
		s.mu.Unlock()
		if err != nil {
			conflict("Couldn't preview: " + err.Error() + ".")
			return
		}
	case "cancel":
		s.free.cancelCheck()
	case "delete":
		// The delete goes to the target its preview was checked at, and
		// only while that is still where the telescope is: holding
		// s.admit keeps a change of target from landing in between.
		s.admit.Lock()
		defer s.admit.Unlock()
		s.mu.Lock()
		_, now, err := s.resolve(s.file.settings)
		s.mu.Unlock()
		if err == nil {
			err = s.free.confirm(in.ID, in.Folders, now)
		}
		if errors.Is(err, errStale) {
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
	localhttp.Respond(w, http.StatusOK, s.free.view())
}

// clearHistory deletes the saved folder's state file, as a reset of sync
// does, so the next pass adopts the files still in the folder and copies
// the rest again. It refuses while copying is on, while a Free up space
// check or delete runs, and while another copy holds the folder's lock.
// A Free up space preview goes back to Start, since its proof is gone.
func (s *server) clearHistory(w http.ResponseWriter) {
	readonly.Check("clear history")
	conflict := func(msg string) {
		localhttp.Respond(w, http.StatusConflict, struct {
			Error string `json:"error"`
		}{msg})
	}
	s.mu.Lock()
	folder := s.file.settings.Folder
	var err error
	switch {
	case !s.setUp():
		err = errNotSetUp
	case s.copy.on():
		err = errors.New("stop copying first")
	case !s.free.forget():
		err = errors.New("wait for Free up space to finish")
	default:
		err = state.Remove(s.stateDir, folder)
		s.preview.discard()
	}
	s.mu.Unlock()
	switch {
	case errors.Is(err, state.ErrLocked):
		conflict("Couldn't clear the history: another copy is using this folder, in another seestar-wormhole window or seestar-copy-cli. Stop that copy, then try again.")
	case err != nil:
		s.log.Warn("clear history", "dest", folder, "err", err)
		conflict("Couldn't clear the history: " + reason(err) + ".")
	default:
		s.log.Info("copy history cleared", "dest", folder)
		localhttp.Respond(w, http.StatusOK, struct{}{})
	}
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
		localhttp.Respond(w, http.StatusInternalServerError, struct {
			Error string `json:"error"`
		}{"Couldn't open the folder picker: " + err.Error() + ". Type the folder's full path instead."})
		return
	}
	localhttp.Respond(w, http.StatusOK, struct {
		Folder string `json:"folder"`
	}{chosen})
}

// saveFile asks where to save the device's file p and saves it there. It
// answers 204 in every case, so the page stays where it is; a failed save
// is shown in the window's message, unless the app is stopping.
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
		if s.native.Alert != nil {
			s.native.Alert("Couldn't save " + name + ": " + reason(err) + ". Try saving it again.")
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// maxDays caps Only files older than N days: 100 years.
const maxDays = 36500

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
