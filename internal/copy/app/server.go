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
	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
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
	stateDir string // the default history folder; Settings may choose another
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
	// rep reports each operation's outcome to the host, numbered from
	// one sequence that the controllers and Browse share.
	rep *reporter

	// admit is held by each change of where the telescope is or what is
	// saved, and by each start of copying, a pass of copying, a preview,
	// a Free up space check or a delete, so none lands between another's
	// checks and its start. It may be held across a save to disk but is
	// never taken by status, views, Busy or the end of any of them.
	admit sync.Mutex
	// started is set, under admit, once copying has started since the app
	// did, from the page or as the app opened.
	started bool

	mu       sync.Mutex
	file     *settingsFile
	browse   *browse.Handler // without statuses until a folder is saved
	sessions *browse.Sessions
	// browseRun is Browse's run for the scan on this network: a new one
	// with each pool and each load of Browse's page.
	browseRun *landiscovery.Run
	// view counts what Browse shows, for the page, which loads its frame
	// again once it moved: each new pool moves it, and so does each new
	// endpoint status finds, such as a route's run connecting.
	view   int
	viewAt Target // the target view last moved for; zero for none
}

func newServer(ctx context.Context, o MountOptions, file *settingsFile) *server {
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &server{
		ctx: ctx, addr: o.Addr, version: o.Version, stateDir: o.StateDir, target: o.Target, file: file, log: log,
		rep: &reporter{report: o.Report},
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
		dial = func(ctx context.Context, t Target) (remote.Session, error) {
			return remote.Dial(ctx, t.Endpoint, log)
		}
	}
	// Browse's dials ask for the endpoint again, so they reach the
	// telescope where it is now. A change of route replaces Browse's pool.
	// Browse's page shows only the error's message, so one on this network
	// carries the Local Network hint.
	s.dial = func(ctx context.Context) (remote.Session, error) {
		t, err := s.target()
		if err != nil {
			return nil, err
		}
		sess, err := establish(ctx, t, func(ctx context.Context) (remote.Session, error) { return dial(ctx, t) })
		if err != nil && t.onLAN() {
			err = withHint(err)
		}
		return sess, err
	}
	// Each pass of copying follows its telescope to where it is now, and
	// copying stops once the target is another telescope.
	s.copy = newController(ctx, o.StateDir, log, dial)
	s.copy.admit, s.copy.target, s.copy.rep = &s.admit, s.target, s.rep
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
	s.preview.at, s.preview.rep = at, s.rep
	s.free = newFreer(ctx, o.StateDir, log, dial)
	s.free.at, s.free.rep = at, s.rep
	// Free up space's outcomes join copy's in Activity, tagged "delete".
	s.free.record = slog.New(newTee(slog.DiscardHandler, s.copy.activity, "delete"))
	if o.Remover != nil {
		s.free.remover = o.Remover
	}
	s.free.resolve = o.Resolve
	s.browse, s.sessions = s.newBrowse(file.settings)
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
// reaches the target. Once a folder is saved it also shows the folder's
// records and what the selection leaves out; before that, Browse shows
// the telescope's files without statuses.
// It is called holding s.mu, or before the server is shared.
func (s *server) newBrowse(set Settings) (*browse.Handler, *browse.Sessions) {
	s.view++
	folder, log := set.Folder, s.log
	// Every pool, old or new, numbers its outcomes from the Mount's one
	// sequence, so an old pool's late failure can't reopen what a new
	// one's success cleared.
	rep := browse.Reporter{
		Report: func(op string, err error, canceled bool, seq uint64) {
			s.rep.send(Outcome{Source: diagnostic.SourceBrowse, Op: op, Err: classified(err), Canceled: canceled, Seq: seq})
		},
		Seq: s.rep.next,
	}
	s.browseRun = &landiscovery.Run{}
	sessions := browse.NewSessions(s.ctx, func(ctx context.Context) (remote.Session, error) {
		s.mu.Lock()
		run := s.browseRun
		s.mu.Unlock()
		return s.dial(landiscovery.WithRun(ctx, run))
	}, log, rep)
	var records func() (map[string]state.Record, error)
	var sel engine.Selector
	if s.setUp() {
		var err error
		if sel, err = selector(set); err != nil {
			log.Warn("browse shows nothing as left out", "err", err)
		}
		history := set.historyDir(s.stateDir)
		records = func() (map[string]state.Record, error) { return state.Load(history, folder, log) }
		if file, err := state.Path(history, folder); err == nil {
			records = browse.CacheRecords(file, records)
		}
	}
	h := browse.NewHandler(browse.Config{
		Addr: s.addr, Sessions: sessions, Records: records, Selector: sel, Log: log, InApp: true, Reporter: rep,
	})
	return h, sessions
}

// errStarted refuses the start as the app opens once copying has started.
var errStarted = errors.New("copying has started since the app opened")

// noTarget is a start refused because the telescope has no target yet.
// It reads as the target's own error.
type noTarget struct{ err error }

func (e noTarget) Error() string { return e.err.Error() }
func (e noTarget) Unwrap() error { return e.err }

// startCopying turns copying on with the saved settings, which discards
// a preview. Holding s.admit keeps a Save or a change of endpoint from
// landing between the check and the start. The start as the app opens,
// opening, is refused once copying has started, even if it has since
// stopped, as a pass of copying once does by itself.
func (s *server) startCopying(opening bool) error {
	_, err := s.startCopyingSeq(opening)
	return err
}

// startCopyingSeq is startCopying, with the sequence number of its
// outcome, drawn under s.admit.
func (s *server) startCopyingSeq(opening bool) (seq uint64, err error) {
	s.admit.Lock()
	defer s.admit.Unlock()
	defer func() { seq = s.rep.next() }()
	if opening && s.started {
		return 0, errStarted
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.setUp() {
		return 0, errNotSetUp
	}
	set, t, err := s.resolve(s.file.settings)
	if err != nil {
		return 0, noTarget{err}
	}
	if err := s.copy.start(set, t); err != nil {
		return 0, err
	}
	s.started = true
	s.preview.discard()
	return 0, nil
}

// startOutcome is a start's outcome.
func startOutcome(err error, seq uint64) Outcome {
	return Outcome{Source: diagnostic.SourceCopy, Op: diagnostic.OpStart, Err: classified(err), Seq: seq}
}

// startPreview runs a preview with the saved settings while copying is
// off. Holding s.admit keeps a Save, a Start or a change of endpoint from
// landing between the checks and the start.
// It returns the sequence number of a refusal, drawn under s.admit.
func (s *server) startPreview() (seq uint64, err error) {
	s.admit.Lock()
	defer s.admit.Unlock()
	defer func() { seq = s.rep.next() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case !s.setUp():
		return 0, errNotSetUp
	case s.copy.on():
		return 0, errors.New("copying is on")
	}
	set, t, err := s.resolve(s.file.settings)
	if err != nil {
		return 0, err
	}
	return 0, s.preview.start(set, t)
}

// copyView is the Copy tab with its preview.
func (s *server) copyView() copyView {
	v := s.copy.view(time.Now())
	v.Preview = s.preview.view()
	return v
}

var errNotSetUp = errors.New("save the copy settings first")

// endpointChanged follows a change of where the telescope is, which the
// caller made holding s.admit: the previews, and a check that failed, go
// back to Start, as their proof was taken elsewhere, a copy once that
// couldn't reach the telescope shows as off, and browse gets a new pool,
// so no request
// reaches the old endpoint through a session already open there.
func (s *server) endpointChanged() {
	s.mu.Lock()
	s.free.clear()
	s.preview.discard()
	s.copy.moved()
	old := s.sessions
	s.browse, s.sessions = s.newBrowse(s.file.settings)
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
		if r.URL.Path == "/browse/" && r.Method == http.MethodGet {
			// Opening Browse is a new run for the scan on this network.
			s.browseRun = &landiscovery.Run{}
		}
		s.mu.Unlock()
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
	HistoryDir   string   `json:"history_dir"`
	Interval     string   `json:"interval"`
	FilesAtOnce  string   `json:"files_at_once"`
	Excludes     []string `json:"excludes"` // the box's lines
	Includes     []string `json:"includes"`
	SkipBuiltIn  bool     `json:"skip_built_in"`
	StartCopying bool     `json:"start_copying"`
	Once         bool     `json:"once"`
	Action       string   `json:"action"` // the copy and delete APIs'
	// The delete API's: Find's options, and Delete's preview and folders.
	Days         string   `json:"days"`
	Trust        bool     `json:"trust"`
	FreeExcludes []string `json:"free_excludes"` // the box's lines
	FreeIncludes []string `json:"free_includes"`
	ID           int      `json:"id"`
	Folders      []string `json:"folders"`
}

func (s *server) status(w http.ResponseWriter) {
	t, err := s.target()
	if err != nil {
		t = Target{}
	}
	s.mu.Lock()
	if t != s.viewAt {
		s.viewAt = t
		s.view++
	}
	set, setUp, view := s.file.settings, s.setUp(), s.view
	s.mu.Unlock()
	// The saved folder's state file, as sync names it.
	var history string
	if setUp {
		history, _ = state.Path(set.historyDir(s.stateDir), set.Folder)
	}
	lines, seq := s.copy.activity.lines()
	localhttp.Respond(w, http.StatusOK, struct {
		Version         string   `json:"version"`
		SetUp           bool     `json:"set_up"`
		Settings        Settings `json:"settings"`
		History         string   `json:"history"`         // "" until set up
		DefaultHistory  string   `json:"default_history"` // the history folder an empty history_dir means
		DefaultExcludes []string `json:"default_excludes"`
		Copy            copyView `json:"copy"`
		Free            freeView `json:"free"`
		Activity        []entry  `json:"activity"` // newest first
		ActivitySeq     int      `json:"activity_seq"`
		Choose          bool     `json:"choose"`      // the window's folder picker is there
		BrowseView      int      `json:"browse_view"` // moves when Browse should load again
	}{s.version, setUp, set, history, s.stateDir, engine.DefaultExcludes, s.copyView(), s.free.view(), lines, seq, s.native.ChooseFolder != nil, view})
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
	s.rep.send(Outcome{Source: diagnostic.SourceCopy, Op: diagnostic.OpTest, Err: err, Canceled: ctx.Err() != nil && err != nil, Seq: s.rep.next()})
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
	// s.admit is released before reporting and answering, on every path.
	s.admit.Lock()
	s.mu.Lock()
	if s.copy.on() {
		s.mu.Unlock()
		s.admit.Unlock()
		localhttp.Respond(w, http.StatusConflict, struct {
			Error string `json:"error"`
		}{"Stop copying to change settings."})
		return
	}
	old, wasSetUp := s.file.settings, s.setUp()
	// The Free up space options are saved by Find, not by this form.
	set.OlderThanDays, set.TrustState = old.OlderThanDays, old.TrustState
	set.FreeExcludes, set.FreeIncludes = old.FreeExcludes, old.FreeIncludes
	if err := s.file.save(set); err != nil {
		s.mu.Unlock()
		seq := s.rep.next()
		s.admit.Unlock()
		s.log.Error("save settings", "err", err)
		s.rep.send(Outcome{Source: diagnostic.SourceCopy, Op: diagnostic.OpSaveSettings, Err: err, Seq: seq})
		// The page shows the reason without the temp file's name.
		localhttp.Respond(w, http.StatusInternalServerError, struct {
			Error string `json:"error"`
		}{"Couldn't save settings: " + reason(err) + ". Your changes have not been saved. Try saving again."})
		return
	}
	// A new folder or history folder replaces browse, which shows its
	// records, and discards both previews, which read them. A new
	// selection replaces browse too, which shows what it leaves out; Free
	// up space has its own patterns.
	moved := !wasSetUp || old.Folder != set.Folder || old.HistoryDir != set.HistoryDir
	changed := moved ||
		!slices.Equal(old.Excludes, set.Excludes) || !slices.Equal(old.Includes, set.Includes) || old.SkipBuiltIn != set.SkipBuiltIn
	if moved {
		s.free.clear()
	}
	var oldSessions *browse.Sessions
	if changed {
		s.preview.discard()
		oldSessions = s.sessions
		s.browse, s.sessions = s.newBrowse(set)
	}
	s.mu.Unlock()
	seq := s.rep.next()
	s.admit.Unlock()
	s.log.Info("settings saved")
	s.rep.send(Outcome{Source: diagnostic.SourceCopy, Op: diagnostic.OpSaveSettings, Seq: seq})
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
		// Both report with nothing held: they release s.admit and s.mu
		// as they return.
		var seq uint64
		seq, err = s.startCopyingSeq(false)
		s.rep.send(startOutcome(err, seq))
	case "stop":
		s.copy.stop()
	case "check":
		s.copy.check()
	case "preview":
		var seq uint64
		seq, err = s.startPreview()
		verb = "preview"
		if err != nil {
			s.rep.send(Outcome{Source: diagnostic.SourceCopy, Op: diagnostic.OpPreview, Err: err, Seq: seq})
		}
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
		errs := map[string]string{}
		days, err := strconv.Atoi(strings.TrimSpace(in.Days))
		if err != nil || days < 0 || days > maxDays {
			errs["days"] = "Enter a whole number of days, 0 or more."
		}
		excludes, msg := checkPatterns(in.FreeExcludes, "preview", "excludes always win, so put the files to free up in Only free up")
		if msg != "" {
			errs["free_excludes"] = msg
		}
		includes, msg := checkPatterns(in.FreeIncludes, "preview", "put the files to keep in Keep on the telescope")
		if msg != "" {
			errs["free_includes"] = msg
		}
		if len(errs) > 0 {
			localhttp.Respond(w, http.StatusBadRequest, fieldErrors{errs})
			return
		}
		// Holding s.admit keeps a Save or a change of endpoint from
		// landing between the read and the start; s.mu and s.admit are
		// released before reporting, logging or answering.
		s.admit.Lock()
		s.mu.Lock()
		// refused ends a Find that didn't start: it reports err as the
		// preview's outcome, after any save of the options.
		refused := func(err error, saved bool) {
			s.mu.Unlock()
			seq := s.rep.next()
			s.admit.Unlock()
			if saved {
				s.rep.send(Outcome{Source: diagnostic.SourceDelete, Op: diagnostic.OpSaveOptions, Seq: seq})
			}
			s.rep.send(Outcome{Source: diagnostic.SourceDelete, Op: diagnostic.OpPreview, Err: err, Seq: seq})
			conflict("Couldn't preview: " + err.Error() + ".")
		}
		if !s.setUp() {
			refused(errNotSetUp, false)
			return
		}
		// A Find refused as busy saves nothing: the running check keeps
		// the options it started with.
		if s.free.running() {
			refused(errFreeBusy, false)
			return
		}
		set, old := s.file.settings, s.file.settings
		set.OlderThanDays, set.TrustState = days, in.Trust
		set.FreeExcludes, set.FreeIncludes = excludes, includes
		saved := false
		if set.OlderThanDays != old.OlderThanDays || set.TrustState != old.TrustState ||
			!slices.Equal(set.FreeExcludes, old.FreeExcludes) || !slices.Equal(set.FreeIncludes, old.FreeIncludes) {
			if err := s.file.save(set); err != nil {
				s.mu.Unlock()
				seq := s.rep.next()
				s.admit.Unlock()
				s.log.Error("save settings", "err", err)
				s.rep.send(Outcome{Source: diagnostic.SourceDelete, Op: diagnostic.OpSaveOptions, Err: err, Seq: seq})
				conflict("Couldn't save the options: " + reason(err) + ". Try again.")
				return
			}
			// The preview shown was checked with the old options, so it
			// goes before the new check starts, even if that fails.
			s.free.clear()
			saved = true
		}
		var t Target
		if set, t, err = s.resolve(set); err == nil {
			err = s.free.find(set, t)
		}
		if err != nil {
			refused(err, saved)
			return
		}
		s.mu.Unlock()
		seq := s.rep.next()
		s.admit.Unlock()
		if saved {
			s.rep.send(Outcome{Source: diagnostic.SourceDelete, Op: diagnostic.OpSaveOptions, Seq: seq})
		}
	case "cancel":
		s.free.cancelCheck()
	case "delete":
		// The delete goes to the target its preview was checked at, and
		// only while that is still where the telescope is: holding
		// s.admit keeps a change of target from landing in between.
		// s.admit is released before reporting or answering. A stale
		// preview is canceled: the deletion neither failed nor ran.
		s.admit.Lock()
		s.mu.Lock()
		_, now, err := s.resolve(s.file.settings)
		s.mu.Unlock()
		if err == nil {
			err = s.free.confirm(in.ID, in.Folders, now)
		}
		seq := s.rep.next()
		s.admit.Unlock()
		if errors.Is(err, errStale) {
			conflict("Preview again before deleting.")
			return
		} else if err != nil {
			s.rep.send(Outcome{Source: diagnostic.SourceDelete, Op: diagnostic.OpDelete, Err: classified(err), Seq: seq})
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
	folder, history := s.file.settings.Folder, s.file.settings.historyDir(s.stateDir)
	var err error
	switch {
	case !s.setUp():
		err = errNotSetUp
	case s.copy.on():
		err = errors.New("stop copying first")
	case !s.free.forget():
		err = errors.New("wait for Free up space to finish")
	default:
		err = state.Remove(history, folder)
		s.preview.discard()
	}
	// The outcome is numbered under s.mu, which orders clears, and
	// reported once it is released.
	seq := s.rep.next()
	s.mu.Unlock()
	s.rep.send(Outcome{Source: diagnostic.SourceCopy, Op: diagnostic.OpClearHistory, Err: classified(err), Seq: seq})
	switch {
	case errors.Is(err, state.ErrLocked):
		conflict("Couldn't clear the history: another copy is using this folder, in another seestar-wormhole window or seestar-wormhole-cli sync. Stop that copy, then try again.")
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
	// A Cancel, which answers "", is neither a failure nor a success.
	s.rep.send(Outcome{Source: diagnostic.SourceCopy, Op: diagnostic.OpChooseFolder, Err: err, Canceled: err == nil && chosen == "", Seq: s.rep.next()})
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
	var alert string
	if err == nil && dst != "" {
		alert, err = s.saveTo(r.Context(), sessions, p, dst)
		if err == nil {
			s.log.Info("file saved", "path", p, "to", dst)
		}
	}
	// A Cancel, which answers "", and a request gone away are neither a
	// failure nor a success.
	s.rep.send(Outcome{
		Source: diagnostic.SourceBrowse, Op: diagnostic.OpSaveFile, Err: classified(err),
		Canceled: err == nil && dst == "" || err != nil && r.Context().Err() != nil, Seq: s.rep.next(),
	})
	if err != nil && r.Context().Err() == nil {
		s.log.Warn("save failed", "path", p, "to", dst, "err", err)
		if alert == "" {
			alert = "Couldn't save " + name + ": " + reason(err) + ". Try saving it again."
		}
		if s.native.Alert != nil {
			s.native.Alert(alert)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// errReservedName is a save to the folder lock's name in the images folder.
var errReservedName = errors.New("the name is kept for the copy lock")

// saveTo saves the device's file p to dst, as [browse.Save] does. A save
// into the saved images folder holds the folder's lock, so it never lands
// during a pass of copying or a delete, and writes through the folder's
// root; it refuses the folder lock's own name. A failure the window words
// itself comes with the alert to show.
func (s *server) saveTo(ctx context.Context, sessions *browse.Sessions, p, dst string) (alert string, err error) {
	s.mu.Lock()
	folder := s.file.settings.Folder
	s.mu.Unlock()
	top, rel, inside := within(folder, dst)
	if !inside {
		return "", browse.Save(ctx, sessions, p, dst)
	}
	if plan.Reserved(filepath.ToSlash(rel)) {
		return "That name is kept for the copy lock. Choose another name or folder.", errReservedName
	}
	root, err := os.OpenRoot(top)
	if err != nil {
		return "", err
	}
	defer root.Close()
	lock, err := state.LockFolder(root)
	if errors.Is(err, state.ErrFolderLocked) {
		return "Another copy or delete is using this folder. Try saving it again.", err
	}
	if err != nil {
		return "", err
	}
	defer lock.Close()
	return "", browse.SaveIn(ctx, sessions, p, root, rel)
}

// within reports whether the file dst is inside folder, after resolving
// the symbolic links in dst's folder, and returns the folder that is
// folder by [os.SameFile] (another spelling, a link or another case) and
// dst relative to it.
func within(folder, dst string) (top, rel string, ok bool) {
	if !filepath.IsAbs(folder) {
		return "", "", false
	}
	want, err := os.Stat(folder)
	if err != nil {
		return "", "", false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(dst))
	if err != nil {
		return "", "", false
	}
	for d := dir; ; {
		if fi, err := os.Stat(d); err == nil && os.SameFile(want, fi) {
			rel, err := filepath.Rel(d, filepath.Join(dir, filepath.Base(dst)))
			return d, rel, err == nil
		}
		up := filepath.Dir(d)
		if up == d {
			return "", "", false
		}
		d = up
	}
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
