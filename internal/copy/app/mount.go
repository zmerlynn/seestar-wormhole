package app

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// MountOptions is what a [Mount] runs with.
type MountOptions struct {
	Addr     netip.AddrPort // the listener of the server that mounts it
	StateDir string         // the CLI's default state folder
	Version  string
	Log      *slog.Logger
	// Target is where the telescope is now, asked again for each start and
	// each dial. Its error says why there is no target now, and the start
	// or dial fails with it. It must not wait on [Mount.Change].
	Target func() (Target, error)
	// Dial opens a session to an endpoint; nil is remote.Dial. Test is
	// Test's check; nil is engine.Test. Remover opens a delete's remover;
	// nil is remote.DialRemover. Tests replace them.
	Dial    func(ctx context.Context, endpoint string) (remote.Session, error)
	Test    func(ctx context.Context, endpoint string) ([]remote.Entry, error)
	Remover func(ctx context.Context, endpoint string) (remote.Remover, error)
}

// Target is where copying, a preview or a delete reaches the telescope:
// its endpoint, host:port, and through the account, the telescope the
// connection selected and that connection's run, which a reconnect
// changes. Device is only compared, never shown or logged.
type Target struct {
	Endpoint string
	Device   string // "" on this network
	Run      uint64 // 0 on this network
}

// String is the endpoint only.
func (t Target) String() string { return t.Endpoint }

// sameTelescope reports whether u reaches the telescope t does: through
// the account the same device, in any run and at any endpoint; on this
// network the same address.
func (t Target) sameTelescope(u Target) bool {
	if t.Device == "" && u.Device == "" {
		return t.Endpoint == u.Endpoint
	}
	return t.Device == u.Device
}

// Mount is the app's API and Browse inside another app's server, which
// owns the page, the token and the checks: it serves /copy/api/<call> and
// /browse/. Its settings are kept in memory only, and it reaches the
// telescope at its Target rather than a saved address.
type Mount struct {
	s        *server
	cancel   context.CancelFunc
	quitOnce sync.Once
}

// NewMount returns a Mount whose controllers live in ctx until Quit.
func NewMount(ctx context.Context, o MountOptions) *Mount {
	ctx, cancel := context.WithCancel(ctx)
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	file := &settingsFile{settings: defaultSettings(), raw: map[string]jsontext.Value{}}
	s := newServer(ctx, o.Addr, "", o.Version, o.StateDir, file, log)
	dial := o.Dial
	if dial == nil {
		dial = func(ctx context.Context, endpoint string) (remote.Session, error) {
			return remote.Dial(ctx, endpoint, log)
		}
	}
	if o.Test != nil {
		s.test = o.Test
	}
	s.target = o.Target
	// Browse's dials ask for the endpoint again, so they reach the
	// telescope where it is now. A change of route replaces Browse's pool.
	s.dial = func(ctx context.Context, _ string) (remote.Session, error) {
		t, err := o.Target()
		if err != nil {
			return nil, err
		}
		return dial(ctx, t.Endpoint)
	}
	// Each pass of copying follows its telescope to where it is now, and
	// copying stops once the target is another telescope.
	s.copy.dial, s.copy.target = dial, o.Target
	// A preview's and a delete's dials go only to the target they were
	// started at, and fail once that changed, even by a reconnect: a
	// delete must not remove files on a telescope it never checked.
	at := func(t Target) error {
		now, err := o.Target()
		if err == nil && now != t {
			err = errMoved
		}
		return err
	}
	s.preview.dial, s.preview.at = dial, at
	s.free.dial, s.free.at = dial, at
	if o.Remover != nil {
		s.free.remover = o.Remover
	}
	return &Mount{s: s, cancel: cancel}
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
// copier to another route or address. Once commit has run, even if it
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
	defer s.endpointChanged()
	return commit()
}

// Test runs Test's check of endpoint, host:port, as the API's Test does.
func (m *Mount) Test(ctx context.Context, endpoint string) error {
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	_, err := m.s.test(ctx, endpoint)
	return err
}

// ServeHTTP serves /copy/api/<call>, as the app serves /app/api/<call>, and
// Browse at /browse/. The caller has checked the Host, the token on an API
// call, and a POST's Origin, Fetch Metadata and content type; Browse makes
// its own checks, since its frame sends no token.
func (m *Mount) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/copy/api/"):
		m.s.api(w, r, strings.TrimPrefix(r.URL.Path, "/copy/api/"), nil)
	case strings.HasPrefix(r.URL.Path, "/browse/"):
		m.s.mu.Lock()
		h := m.s.browse
		m.s.mu.Unlock()
		if h == nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		http.StripPrefix("/browse", h).ServeHTTP(w, r)
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

// Busy reports whether copying is on, a preview runs or Free up space is
// checking or deleting, which a change of where the telescope is must
// wait for.
func (m *Mount) Busy() bool { return m.s.copy.on() || m.s.preview.running() || m.s.free.running() }

// Quit closes Browse's session, then stops copying, the preview and Free
// up space and waits for them. Later calls do nothing.
func (m *Mount) Quit() {
	m.quitOnce.Do(func() {
		m.cancel()
		m.s.close()
		m.s.copy.quit()
		m.s.preview.quit()
		m.s.free.quit()
	})
}

// NormalizeAddress checks a telescope address as typed, host or host:port,
// and returns it as host:port, with port 445 for a bare host.
func NormalizeAddress(s string) (string, bool) { return normalizeAddress(s) }
