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

	"github.com/zmerlynn/seestar-wormhole/internal/copy/engine"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// MountOptions is what a [Mount] runs with.
type MountOptions struct {
	Addr     netip.AddrPort // the listener of the server that mounts it
	StateDir string         // the CLI's default state folder
	Version  string
	Log      *slog.Logger
	// Endpoint is where the telescope is, host:port, asked again for each
	// start and each dial. Its error says why there is no endpoint now,
	// and the start or dial fails with it. It must not wait on [Mount.Change].
	Endpoint func() (string, error)
	// Dial opens a session to an endpoint; nil is remote.Dial. Test is
	// Test's check; nil is engine.Test. Remover opens a delete's remover;
	// nil is remote.DialRemover. Tests replace them.
	Dial    func(ctx context.Context, endpoint string) (remote.Session, error)
	Test    func(ctx context.Context, endpoint string) ([]remote.Entry, error)
	Remover func(ctx context.Context, endpoint string) (remote.Remover, error)
}

// Mount is the app's API and Browse inside another app's server, which
// owns the page, the token and the checks: it serves /copy/api/<call> and
// /browse/. Its settings are kept in memory only, and it reaches the
// telescope at its Endpoint rather than a saved address.
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
		dial = func(ctx context.Context, endpoint string) (remote.Session, error) { return remote.Dial(ctx, endpoint) }
	}
	if o.Test != nil {
		s.test = o.Test
	} else {
		s.test = engine.Test
	}
	s.endpoint = o.Endpoint
	// Browse's dials and copying's ask for the endpoint again, so a pass
	// reaches the telescope where it is now: through the account, a
	// reconnect may map it elsewhere. A change of route waits for copying
	// to stop, and replaces Browse's pool.
	s.dial = func(ctx context.Context, _ string) (remote.Session, error) {
		endpoint, err := o.Endpoint()
		if err != nil {
			return nil, err
		}
		return dial(ctx, endpoint)
	}
	// A preview's and a delete's dials go only to the endpoint they were
	// started at, and fail once the telescope is elsewhere: a delete must
	// not remove files on a telescope it never checked.
	at := func(endpoint string) error {
		now, err := o.Endpoint()
		if err == nil && now != endpoint {
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
// endpoint is no longer the one it started at.
var errMoved = errors.New("the telescope's address changed; preview again")

// ErrBusy refuses a change of route while copying is on or Free up space
// checks or deletes.
var ErrBusy = errors.New("copying or Free up space is running")

// Change commits a change of where the telescope is: commit runs while
// no copying, preview or Free up space can start. moves reports whether
// the change takes the copier to another route or address; one that does
// is refused with ErrBusy, without calling commit, while copying is on or
// Free up space checks or deletes. Once commit has run, even if it failed
// partway, the copy and Free up space previews go back to Start and Browse
// gets a new session pool. moves and commit may take the caller's locks,
// but must not call back into the Mount.
func (m *Mount) Change(moves func() bool, commit func() error) error {
	s := m.s
	s.admit.Lock()
	defer s.admit.Unlock()
	if moves() && (s.copy.on() || s.free.running()) {
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

// Busy reports whether copying is on or Free up space is checking or
// deleting, which a change of where the telescope is must wait for.
func (m *Mount) Busy() bool { return m.s.copy.on() || m.s.free.running() }

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
