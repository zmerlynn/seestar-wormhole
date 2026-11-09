package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/guard"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// MountOptions is what a [Mount] runs with.
type MountOptions struct {
	Addr     netip.AddrPort // the listener of the server that mounts it
	StateDir string         // the file commands' default history folder
	Version  string
	Log      *slog.Logger
	// Target is where the telescope is now, asked again for each start and
	// each dial. Its error says why there is no target now, and the start
	// or dial fails with it. It must not wait on [Mount.Change].
	Target func() (Target, error)
	// Dial opens a session at a target; nil is remote.Dial to its
	// endpoint. Remover opens a delete's remover there; nil is
	// remote.DialRemover to its endpoint. The host chooses the route from
	// the target's Run, never from its endpoint's text. Test is Test's
	// check; nil is engine.Test. Reach is [Mount.Reach]'s; nil is
	// remote.Reach. Both check an address on this network. Tests replace
	// them.
	Dial func(ctx context.Context, t Target) (remote.Session, error)
	// Resolve, if set, opens a Free up space preview's session at t and
	// returns the target it reached: on this network, a telescope saved
	// with its serial is wherever that serial answers now. The preview,
	// and the delete that follows it, are bound to that target, and
	// Remover is given it exactly. nil is Dial at t.
	Resolve func(ctx context.Context, t Target) (remote.Session, Target, error)
	Test    func(ctx context.Context, endpoint string) ([]remote.Entry, error)
	Reach   func(ctx context.Context, endpoint string) error
	Remover func(ctx context.Context, t Target) (remote.Remover, error)
	// Settings are the copy settings saved before, or nil for none yet.
	// Save keeps new ones, and they take effect only if it succeeds; nil
	// keeps them in memory only. It is called with the Mount's locks
	// held, so it must not call back into the Mount.
	Settings *Settings
	Save     func(Settings) error
	// Native is the host window's dialogs; nil, or a nil dialog, without
	// a window.
	Native *Native
	// Name, if set, is how the page names t's telescope, as "<model>
	// <serial>" or for an address "<model> <host> <port>": the base of
	// the folder suggested for it and the name in the offer of an
	// address's folder. It is shown on the page and suggested as a folder
	// name only, never logged. nil is "Seestar".
	Name func(t Target) string
	// KnowSerial, if set, saves that the telescope saved by t's address
	// only is the one with serial t.Heard, so that t's telescope is keyed
	// by that serial from then on. Accepting the offer of the address's
	// folder to a serial heard there calls it, first; without it, no such
	// offer is made. It is called with the Mount's locks held, so it must
	// not call back into the Mount.
	KnowSerial func(t Target) error
	// Report, if set, gets the outcome of each Copy, Browse and Free up
	// space operation as it ends, numbered in the order they ended. It is
	// called with none of the Mount's locks held, from the goroutine the
	// operation ended on, so a slow Report delays only that operation.
	Report func(Outcome)
}

// Native is a window's own dialogs. Each may be called from any goroutine
// and blocks until the user answers.
type Native struct {
	// ChooseFolder shows a folder picker that starts in dir, if dir is
	// not empty, and returns the folder chosen, or "" on Cancel. With it,
	// the page offers Choose… beside the folder it has typed.
	ChooseFolder func(dir string) (string, error)
	// SaveFile shows a save dialog for a file named name and returns the
	// path chosen, or "" on Cancel. With it, Browse saves a download
	// itself, since a window's webview may not save one.
	SaveFile func(name string) (string, error)
	// Alert shows an error message; nil logs it only.
	Alert func(message string)
}

// Target is where copying, a preview or a delete reaches the telescope:
// on this network its endpoint, host:port; through the account, a label
// for display only, the telescope the connection selected and that
// connection's run, which a reconnect changes. Device is only compared,
// never shown or logged.
type Target struct {
	Endpoint string
	Device   string // "" on this network
	Run      uint64 // 0 on this network
	// Serial is the serial number a scan saw at Endpoint on this network,
	// or "". It is compared only, never shown or logged.
	Serial string
	// Heard is the serial the latest scan or Test heard at Endpoint on
	// this network, when the telescope is saved by its address only, or
	// "". It only offers the address's folder to that serial (see
	// MountOptions.KnowSerial), and is never shown or logged.
	Heard string
	// Telescope is the telescope's key in the settings' telescopes (see
	// SerialKey and AddressKey), or "" while it isn't known. It chooses
	// the folder, and is compared only, never shown or logged. The host
	// sets it even with an error, when it knows the telescope saved.
	Telescope string
}

// String is the endpoint only.
func (t Target) String() string { return t.Endpoint }

// SavedLabel is what logs and error text call a telescope on this network
// saved with its serial, in place of its address: a scan may have found
// it there.
const SavedLabel = "the saved telescope"

// logged is how logs, Activity and error text name t's endpoint.
func (t Target) logged() string {
	if t.onLAN() && t.Serial != "" {
		return SavedLabel
	}
	return t.Endpoint
}

// onLAN reports whether t is an address on this network.
func (t Target) onLAN() bool { return t.Device == "" && t.Run == 0 }

// sameTelescope reports whether u reaches the telescope t does: through
// the account the same device, in any run and at any endpoint; with no
// device on either, the same address in the same run, which on this
// network is always 0, or on this network the same serial at any address.
// Another key is another telescope, whose folder is another.
func (t Target) sameTelescope(u Target) bool {
	if t.Telescope != u.Telescope {
		return false
	}
	if t.Device == "" && u.Device == "" {
		return t.Endpoint == u.Endpoint && t.Run == u.Run || t.moved(u)
	}
	return t.Device == u.Device
}

// moved reports whether u is t's telescope on this network at another
// address: both carry the same serial.
func (t Target) moved(u Target) bool {
	return t.onLAN() && u.onLAN() && t.Serial != "" && t.Serial == u.Serial
}

// Mount is the app's API and Browse inside another app's server, which
// owns the page, the token and the checks: it serves /copy/api/<call> and
// /browse/. Its host keeps its settings, and it reaches the telescope at
// its Target rather than a saved address.
type Mount struct {
	s        *server
	ctx      context.Context
	cancel   context.CancelFunc
	quitOnce sync.Once
	mu       sync.Mutex // guards quitting against Start
	quitting bool
	starting sync.WaitGroup
}

// NewMount returns a Mount whose controllers live in ctx until Quit.
// Nothing runs until Start.
func NewMount(ctx context.Context, o MountOptions) *Mount {
	ctx, cancel := context.WithCancel(ctx)
	file := &settingsFile{settings: DefaultSettings(), write: o.Save}
	if o.Settings != nil {
		file.settings, file.exists = *o.Settings, true
	}
	return &Mount{s: newServer(ctx, o, file), ctx: ctx, cancel: cancel}
}

// Start turns copying on as the app opens, once there is a target, if the
// settings say to. The host calls it once it is up; Quit waits for it.
// After Quit it does nothing.
func (m *Mount) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.s.mu.Lock()
	want := m.s.wantsStart()
	m.s.mu.Unlock()
	if m.quitting || !want {
		return
	}
	m.starting.Go(func() { m.s.startWhenReady(m.ctx, startEvery) })
}

// startEvery is how often copying that starts when the app opens looks for
// its target.
var startEvery = time.Second

// startWhenReady turns copying on as the app opens, once there is a
// target: through the account it waits for the connection and never starts
// it. It gives up once copying has started since the app opened, even from
// the page, the setting is off, the app quits, or a start fails with a
// target there. Whether to wait is decided by the look that refused the
// start, not by a second look, which may find a target that came since.
func (s *server) startWhenReady(ctx context.Context, every time.Duration) {
	defer guard.Recover(s.log, "starting copying")
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		s.mu.Lock()
		want := s.wantsStart()
		s.mu.Unlock()
		if !want {
			return
		}
		seq, err := s.startCopyingSeq(true)
		if errors.Is(err, errStarted) {
			return
		}
		// A start that waits for its target reports nothing yet.
		if err == nil || !errors.As(err, new(noTarget)) {
			if err != nil {
				s.log.Error("start copying", "err", err)
			}
			s.rep.send(startOutcome(err, seq))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// errMoved fails a preview's or a delete's dial once the telescope's
// target is no longer the one it started at.
var errMoved = errors.New("the connection to the telescope changed; preview again")

// ErrBusy refuses a change of route while copying or Free up space is
// running.
var ErrBusy = errors.New("copying or Free up space is running")

// Change commits a change of where the telescope is, or of the connection
// that reaches it: commit runs while no copying, preview or Free up space
// can start. It is refused with ErrBusy, without calling commit, while a
// pass of copying, a preview, or a Free up space check or delete runs, and
// also while copying is on if moves reports that the change takes the
// copier to another route or address, and with ErrClaim, also without
// calling commit, when the telescope saved before couldn't keep Folder as
// its own. Once commit has run, even if it
// failed partway, the copy and Free up space previews go back to Start and
// Browse gets a new session pool. moves and commit may take the caller's
// locks, but must not call back into the Mount.
func (m *Mount) Change(moves func() bool, commit func() error) error {
	s := m.s
	s.admit.Lock()
	defer s.admit.Unlock()
	if s.copy.passing() || s.preview.running() || s.free.running() || moves() && s.copy.on() {
		return ErrBusy
	}
	// The telescope saved before keeps Folder: a change may make another
	// telescope the first one known. Without that save the change is
	// refused, with ErrClaim, before commit.
	s.mu.Lock()
	err := s.claim(s.current().Telescope)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	defer s.endpointChanged()
	return commit()
}

// Reach checks that the telescope answers at endpoint, host:port, an
// address on this network, as a route's Save does after the page's Test:
// it connects and sets up a session, and lists nothing. Like Test, it
// waits out macOS's Local Network prompt.
func (m *Mount) Reach(ctx context.Context, endpoint string) error {
	return m.s.reachLAN(ctx, endpoint)
}

// ServeHTTP serves /copy/api/<call> and Browse at /browse/. The caller has
// checked the Host, the token on an API call, and a POST's Origin, Fetch
// Metadata and content type; Browse makes its own checks, since its frame
// sends no token.
func (m *Mount) ServeHTTP(w http.ResponseWriter, r *http.Request) { m.s.ServeHTTP(w, r) }

// Busy reports whether copying is on, a preview runs or Free up space is
// checking or deleting, which a change of where the telescope is must
// wait for.
func (m *Mount) Busy() bool { return m.s.copy.on() || m.s.preview.running() || m.s.free.running() }

// Quit closes Browse's session, then stops starting copying, copying, the
// preview and Free up space and waits for them. Later calls do nothing.
func (m *Mount) Quit() {
	m.quitOnce.Do(func() {
		m.mu.Lock()
		m.quitting = true
		m.mu.Unlock()
		m.cancel()
		m.starting.Wait()
		m.s.close()
		m.s.copy.quit()
		m.s.preview.quit()
		m.s.free.quit()
	})
}

// NormalizeAddress checks a telescope address as typed, host or host:port,
// and returns it as host:port, with port 445 for a bare host.
func NormalizeAddress(s string) (string, bool) { return normalizeAddress(s) }
