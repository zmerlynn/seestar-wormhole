package ui

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json/v2"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/icon"
	"github.com/zmerlynn/seestar-wormhole/internal/localhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var assets embed.FS

// hostOS is the GOOS whose wording Save's failures get; tests replace it.
var hostOS = runtime.GOOS

type server struct {
	host, token string
	version     string
	controller  *controller
	storage     *storage
	mu          sync.Mutex
	settings    Settings
	quit        chan struct{}
	quitOnce    sync.Once
	activate    func()
	native      bool
	// copy serves copy's API under /copy/api/ and Browse under /browse/;
	// nil serves neither.
	copy *copyapp.Mount
	// routeSaved opens Copy, Browse and Free up space: the account route
	// has connected, or an address on this network passed Test and was
	// saved. It is guarded by mu and never goes back.
	routeSaved bool
}
type input struct {
	Settings Settings `json:"settings"`
	Password string   `json:"password"`
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Browse is a same-origin frame of /browse/, so the page may frame
	// itself and be framed only by itself.
	localhttp.SetHeaders(w.Header(), false)
	fail := func(code int) { http.Error(w, "Request rejected", code) }
	if r.Host != s.host {
		fail(http.StatusForbidden)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/browse/") {
		// The frame sends no token; Browse makes its own Host, Fetch
		// Metadata and method checks and serves only GET and HEAD.
		if s.copy == nil {
			fail(http.StatusNotFound)
			return
		}
		// A download from the telescope may take longer than the
		// server's write timeout, which bounds every other response.
		http.NewResponseController(w).SetWriteDeadline(time.Time{})
		s.copy.ServeHTTP(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/copy/api/") {
		if s.copy == nil {
			fail(http.StatusNotFound)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Seestar-Token")), []byte(s.token)) != 1 {
			fail(http.StatusForbidden)
			return
		}
		if r.Method == "POST" && !s.allowedPost(w, r) {
			return
		}
		s.copy.ServeHTTP(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != "GET" && r.Method != "HEAD" {
			fail(http.StatusMethodNotAllowed)
			return
		}
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		if path != "/index.html" && path != "/app.js" && path != "/style.css" && path != "/icon.svg" {
			fail(http.StatusNotFound)
			return
		}
		b, err := assets.ReadFile("web" + path)
		if path == "/icon.svg" {
			b, err = icon.SVG, nil
		}
		if err != nil {
			fail(404)
			return
		}
		contentType := "text/html; charset=utf-8"
		if path == "/app.js" {
			contentType = "text/javascript; charset=utf-8"
		}
		if path == "/style.css" {
			contentType = "text/css; charset=utf-8"
		}
		if path == "/icon.svg" {
			contentType = "image/svg+xml"
		}
		w.Header().Set("Content-Type", contentType)
		if r.Method != "HEAD" {
			w.Write(b)
		}
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Seestar-Token")), []byte(s.token)) != 1 {
		fail(http.StatusForbidden)
		return
	}
	read := r.URL.Path == "/api/status" || r.URL.Path == "/api/instance"
	if read {
		if r.Method != "GET" {
			fail(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/api/instance" {
			if s.activate != nil {
				w.Header().Set("X-Seestar-Window", "1")
			}
			localhttp.Respond(w, http.StatusOK, struct {
				Instance string `json:"instance"`
			}{"seestar-wormhole-ui-v1"})
			return
		}
		s.mu.Lock()
		settings := s.settings
		s.mu.Unlock()

		view := s.controller.view()
		effective := settings
		if view.Active {
			effective = view.settings
		}
		ports, _, _ := effective.validate()
		defaults, _ := proxyrun.MergePorts(nil)
		events, path, logFailed := s.controller.log.snapshot()
		unlocked, reach := s.copyState()
		localhttp.Respond(w, http.StatusOK, struct {
			CopyBusy  bool               `json:"copy_busy"`
			Settings  pageSettings       `json:"settings"`
			Version   string             `json:"version"`
			Effective diagnosticSettings `json:"effective"`
			runView
			PortDefaults []proxyrun.Port `json:"port_defaults"`
			Password     passwordInfo    `json:"password"`
			Native       bool            `json:"native"`
			Events       []event         `json:"events"`
			LogPath      string          `json:"log_path"`
			LogFailed    bool            `json:"log_failed"`
			CopyUnlocked bool            `json:"copy_unlocked"`
			CopyReach    string          `json:"copy_reach"`
			Files        settingsFiles   `json:"files"`
		}{s.copy != nil && s.copy.Busy(), pageSettingsFor(settings), s.version, diagnosticSettingsFor(effective, ports), view, defaults, s.controller.credentials.state(settings.Email), s.native, events, path, logFailed,
			unlocked, reach, s.settingsFiles()})
		return
	}
	if r.Method != "POST" {
		fail(http.StatusMethodNotAllowed)
		return
	}
	if !s.allowedPost(w, r) {
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, localhttp.MaxJSON))
	if err != nil {
		fail(http.StatusRequestEntityTooLarge)
		return
	}
	var in input
	if err = json.Unmarshal(b, &in); err != nil {
		var numeric *json.SemanticError
		if errors.As(err, &numeric) && strings.HasPrefix(string(numeric.JSONPointer), "/settings/ports/") {
			s.rejectAction(w, &ActionError{Code: "ports", Field: "ports"})
		} else {
			fail(http.StatusBadRequest)
		}
		return
	}
	in.Settings.Email = strings.TrimSpace(strings.ReplaceAll(in.Settings.Email, " ", ""))
	switch r.URL.Path {
	case "/api/activate":
		if s.activate == nil {
			fail(http.StatusNotFound)
			return
		}
		s.activate()
	case "/api/start":
		// Connect chooses the account route, which moves the copier when
		// this network was the route.
		err = s.changeRoute(func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.settings.Route == routeLAN
		}, func() error {
			s.mu.Lock()
			in.Settings.Route, in.Settings.LANAddress = routeAccount, s.settings.LANAddress
			s.mu.Unlock()
			if err := s.controller.start(in.Settings, in.Password); err != nil {
				return err
			}
			s.mu.Lock()
			// Accepting Connect is not a successful save. Keep the authoritative
			// pair for its account until the run reaches its save boundary.
			s.retainSavedPair(&in.Settings)
			s.settings = in.Settings
			s.mu.Unlock()
			return nil
		})
	case "/api/stop":
		s.controller.stop()

	case "/api/password/save":
		s.mu.Lock()
		in.Settings.Route, in.Settings.LANAddress = s.settings.Route, s.settings.LANAddress
		s.mu.Unlock()
		if _, _, err = in.Settings.validate(); err == nil {
			generation := s.controller.credentials.nextGeneration()
			err = s.controller.credentials.rememberAt(r.Context(), in.Settings, in.Password, generation)
			if err == nil {
				err = s.controller.persistPassword(in.Settings, generation)
			}
			if s.controller.credentials.generationID() == generation {
				_, notice := s.controller.credentials.info()
				s.controller.record(notice)
			}
		}
	case "/api/forget":
		generation := s.controller.credentials.nextGeneration()
		err = s.controller.credentials.forgetAt(r.Context(), in.Settings.Email, generation)
		if err == nil {
			err = s.controller.persistRemoval(generation)
		}
		if s.controller.credentials.generationID() == generation {
			_, notice := s.controller.credentials.info()
			s.controller.record(notice)
		}
	case "/api/route":
		if in.Settings.Route != routeAccount && in.Settings.Route != routeLAN {
			fail(http.StatusBadRequest)
			return
		}
		err = s.saveRoute(r.Context(), in.Settings.Route, in.Settings.LANAddress)
	case "/api/retry-storage":
		err = s.controller.retrySave()
	case "/api/quit":
		if s.copy != nil {
			s.copy.Quit()
		}
		s.controller.quit()
		// Cleanup has joined; flush the response before shutting down HTTP.
		localhttp.Respond(w, http.StatusOK, struct {
			OK bool `json:"ok"`
		}{true})
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		s.quitOnce.Do(func() { close(s.quit) })
		return
	default:
		fail(http.StatusNotFound)
		return
	}
	if err != nil {
		s.rejectAction(w, err)
		return
	}
	localhttp.Respond(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{true})
}

// allowedPost checks a POST's Origin, Fetch Metadata and content type, for
// the wormhole's API and copy's alike, and answers it if they fail.
func (s *server) allowedPost(w http.ResponseWriter, r *http.Request) bool {
	fail := func(code int) { http.Error(w, "Request rejected", code) }
	if r.Header.Get("Origin") != "http://"+s.host {
		fail(http.StatusForbidden)
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		fail(http.StatusForbidden)
		return false
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

// saveRoute saves how the copier reaches the telescope. On this network
// it needs an address, kept as host:port, that the telescope answers at:
// the page's Test has listed its share, and this checks again, more
// cheaply, that it answers, since the page's word isn't proof; the
// account route keeps the saved address for a later switch back. It is
// refused while the account route runs or copying or deleting does, as
// checked again after the Test; a refused or failed save keeps the route
// that was saved.
func (s *server) saveRoute(ctx context.Context, route, address string) error {
	if route == routeLAN {
		normalized, ok := copyapp.NormalizeAddress(address)
		if !ok {
			return &ActionError{Code: "lan_address", Field: "lan_address"}
		}
		address = normalized
		// Reach holds no lock: it waits on the network.
		if s.copy != nil {
			if err := s.copy.Reach(ctx, address); err != nil {
				return &ActionError{Code: "lan_unreachable", Field: "lan_address", hint: copyapp.LocalNetworkHint(hostOS, err)}
			}
		}
	}
	return s.changeRoute(func() bool { return true }, func() error { return s.controller.persistRoute(route, address) })
}

// changeRoute commits a change of how the copier reaches the telescope
// inside copy's admission, so no copying or Free up space starts on the
// old route meanwhile. A change that moves the copier is refused while
// copying or Free up space runs. Without copy mounted it only commits.
//
// Locks are taken in this order: copy's admission, then the controller's
// saveMu, then its mu or s.mu. copy's endpoint takes s.mu and the
// controller's mu inside copy's own locks, so neither is held while
// calling into copy.
func (s *server) changeRoute(moves func() bool, commit func() error) error {
	if s.copy == nil {
		return commit()
	}
	err := s.copy.Change(moves, commit)
	if errors.Is(err, copyapp.ErrBusy) {
		return &ActionError{Code: "route_locked"}
	}
	return err
}

// Copier endpoint errors, shown after "Couldn't start copying: ".
var (
	errNotConnected = errors.New("Not connected. Go to Connection to reconnect")
	errSMBOff       = errors.New("SMB is off. Turn on its port in Connection > Advanced")
	errNoLANAddress = errors.New("Save the telescope's address on the Connection tab first")
)

// copyState reports whether Copy, Browse and Free up space are open, and
// why the copier has no target now: "not_connected", "smb_off" or
// "no_lan_address", or "" when it has one.
func (s *server) copyState() (unlocked bool, reach string) {
	if s.copy == nil {
		return false, ""
	}
	_, err := s.copyTarget()
	switch {
	case errors.Is(err, errNotConnected):
		reach = "not_connected"
	case errors.Is(err, errSMBOff):
		reach = "smb_off"
	case errors.Is(err, errNoLANAddress):
		reach = "no_lan_address"
	}
	_, _, _, connected := s.controller.connected()
	s.mu.Lock()
	defer s.mu.Unlock()
	if connected && s.settings.Route != routeLAN {
		s.routeSaved = true
	}
	return s.routeSaved, reach
}

// routeSavedIn reports whether settings loaded from config.json record an
// established route. A saved email or address alone is not one.
func routeSavedIn(settings Settings) bool { return settings.RouteEstablished }

// settingsFiles are the paths Activity's Files shows.
type settingsFiles struct {
	Config string `json:"config"`
	Copy   string `json:"copy"`
}

func (s *server) settingsFiles() settingsFiles {
	root := s.storage.root
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return settingsFiles{filepath.Join(root, "config.json"), filepath.Join(root, copyName)}
}

// copyTarget is where the copier reaches the telescope now. On this
// network it is the saved address. Through the account it is the proxy's
// local port for the device's SMB port, with the telescope the run
// selected and the run's number, and only while the proxy is connected:
// the copier never starts or holds the device session itself.
func (s *server) copyTarget() (copyapp.Target, error) {
	if copyTargetHook != nil {
		copyTargetHook()
	}
	s.mu.Lock()
	settings := s.settings
	s.mu.Unlock()
	run, device, id, ok := s.controller.connected()
	endpoint, err := copyEndpointFor(settings, run, ok)
	if err != nil || settings.Route == routeLAN {
		return copyapp.Target{Endpoint: endpoint}, err
	}
	return copyapp.Target{Endpoint: endpoint, Device: device, Run: id}, nil
}

// copyEndpointFor is copyEndpoint for the saved settings and the run's,
// which is connected or not.
func copyEndpointFor(settings, run Settings, connected bool) (string, error) {
	if settings.Route == routeLAN {
		if address, ok := copyapp.NormalizeAddress(settings.LANAddress); ok {
			return address, nil
		}
		return "", errNoLANAddress
	}
	if !connected {
		return "", errNotConnected
	}
	ports, _, err := run.validate()
	if err != nil {
		return "", errNotConnected
	}
	// The proxy listens on its bind address; a wildcard or loopback bind
	// is reached on loopback.
	host := netip.AddrFrom4([4]byte{127, 0, 0, 1})
	if addr, err := netip.ParseAddr(run.Bind); err == nil && !addr.IsUnspecified() {
		host = addr.Unmap()
	}
	for _, p := range ports {
		if p.Device == 445 {
			return netip.AddrPortFrom(host, p.Local).String(), nil
		}
	}
	return "", errSMBOff
}

func (s *server) rejectAction(w http.ResponseWriter, err error) {
	e := actionError(err)
	s.controller.log.addFailure(errorCopy(e.Code), err)
	localhttp.Respond(w, http.StatusBadRequest, struct {
		*ActionError
		Message string `json:"message"`
	}{e, errorCopy(e.Code) + e.hint})
}

func (s *server) saveSettings(settings Settings) error {
	// The page submits known choices only; retain the file's future members.
	settings.Unknown = s.storage.settingsUnknown.Unknown
	settings.Ports = append([]proxyrun.Port(nil), settings.Ports...)
	for i, port := range settings.Ports {
		for _, saved := range s.storage.settingsUnknown.Ports {
			if saved.Device == port.Device {
				settings.Ports[i].Unknown = saved.Unknown
				break
			}
		}
	}
	err := s.storage.write("config.json", settings)
	s.mu.Lock()
	// A failed clear keeps the same account's prior selection visible.
	// Other fields still project the current account, including a successful native password save
	// whose settings write failed. The controller owns the retry snapshot.
	if err != nil {
		s.retainSavedPair(&settings)
	}
	s.settings = settings
	if err == nil && settings.RouteEstablished {
		s.routeSaved = true
	}
	s.mu.Unlock()
	return err
}

// retainSavedPair projects only a pair already saved for this normalized account.
// The caller holds s.mu; accepting a draft or a failed write cannot save a pair.
func (s *server) retainSavedPair(settings *Settings) {
	normalize := func(email string) string { return strings.TrimSpace(strings.ReplaceAll(email, " ", "")) }
	if normalize(settings.Email) == normalize(s.settings.Email) {
		settings.Serial, settings.Model = s.settings.Serial, s.settings.Model
	} else {
		settings.Serial, settings.Model = "", ""
	}
}

type pageSettings struct {
	Settings
	IdleMinutes float64 `json:"idle_minutes"`
	ChoiceLabel string  `json:"choice_label"`
}

func pageSettingsFor(s Settings) pageSettings {
	d, _ := time.ParseDuration(s.Idle)
	return pageSettings{s, d.Minutes(), (cloudhttp.Device{Serial: s.Serial, Model: s.Model}).ChoiceLabel()}
}

// One allowlisted projection feeds both Settings and the copied summary.
// It contains no account, device, address, credential, token, or path.
type diagnosticSettings struct {
	Idle      diagnosticIdle    `json:"idle"`
	Listen    string            `json:"listen"`
	Discovery bool              `json:"discovery"`
	Ports     map[uint16]uint16 `json:"ports"`
}
type diagnosticIdle struct {
	Mode    string  `json:"mode"`
	Minutes float64 `json:"minutes"`
}

func diagnosticSettingsFor(s Settings, ports []proxyrun.Port) diagnosticSettings {
	d, _ := time.ParseDuration(s.Idle)
	v := diagnosticSettings{Idle: diagnosticIdle{Mode: "keep_connected", Minutes: d.Minutes()}, Listen: "other", Discovery: s.Discovery, Ports: make(map[uint16]uint16)}
	if d > 0 {
		v.Idle.Mode = "on_demand"
	}
	if addr, err := netip.ParseAddr(s.Bind); err == nil {
		addr = addr.Unmap()
		if addr.IsUnspecified() {
			v.Listen = "all_networks"
		} else if addr.IsLoopback() {
			v.Listen = "this_computer"
		}
	} else if s.Bind == "" {
		v.Listen = "all_networks"
	}
	defaults, _ := proxyrun.MergePorts(nil)
	for _, p := range defaults {
		v.Ports[p.Device] = 0
	}
	for _, p := range ports {
		v.Ports[p.Device] = p.Local
	}
	return v
}
