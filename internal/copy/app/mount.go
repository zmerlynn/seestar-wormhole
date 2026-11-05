package app

import (
	"context"
	"encoding/json/jsontext"
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
	// and the start or dial fails with it.
	Endpoint func() (string, error)
	// Dial opens a session to an endpoint; nil is remote.Dial. Test is
	// Test's check; nil is engine.Test. Tests replace both.
	Dial func(ctx context.Context, endpoint string) (remote.Session, error)
	Test func(ctx context.Context, endpoint string) ([]remote.Entry, error)
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
	// Every dial asks for the endpoint again, so a pass reaches the
	// telescope where it is now.
	s.dial = func(ctx context.Context, _ string) (remote.Session, error) {
		endpoint, err := o.Endpoint()
		if err != nil {
			return nil, err
		}
		return dial(ctx, endpoint)
	}
	remover := s.free.remover
	s.free.remover = func(ctx context.Context, _ string) (remote.Remover, error) {
		endpoint, err := o.Endpoint()
		if err != nil {
			return nil, err
		}
		return remover(ctx, endpoint)
	}
	return &Mount{s: s, cancel: cancel}
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
