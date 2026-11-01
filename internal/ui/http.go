package ui

import (
	"crypto/subtle"
	"embed"
	"encoding/json/v2"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/icon"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var assets embed.FS

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
}
type input struct {
	Settings Settings `json:"settings"`
	Password string   `json:"password"`
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fail := func(code int) { http.Error(w, "Request rejected", code) }
	if r.Host != s.host {
		fail(http.StatusForbidden)
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
			s.respond(w, struct {
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
		s.respond(w, struct {
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
		}{pageSettingsFor(settings), s.version, diagnosticSettingsFor(effective, ports), view, defaults, s.controller.credentials.state(settings.Email), s.native, events, path, logFailed})
		return
	}
	if r.Method != "POST" {
		fail(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "http://"+s.host {
		fail(http.StatusForbidden)
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
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
		err = s.controller.start(in.Settings, in.Password)
		if err == nil {
			s.mu.Lock()
			s.settings = in.Settings
			s.mu.Unlock()
		}
	case "/api/change-telescope":
		err = s.controller.changeTelescope()

	case "/api/stop":
		s.controller.stop()

	case "/api/password/save":
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
	case "/api/retry-storage":
		err = s.controller.retrySave()
	case "/api/quit":
		s.controller.quit()
		// Cleanup has joined; flush the response before shutting down HTTP.
		s.respond(w, struct {
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
	s.respond(w, struct {
		OK bool `json:"ok"`
	}{true})
}
func (s *server) rejectAction(w http.ResponseWriter, err error) {
	s.controller.log.addFailure(errorCopy(actionError(err).Code), err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	s.respond(w, struct {
		*ActionError
		Message string `json:"message"`
	}{actionError(err), errorCopy(actionError(err).Code)})
}

func (s *server) respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "Response unavailable", 500)
		return
	}
	w.Write(b)
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
	// Publish saved fields only after persistence succeeds. Failed writes retain
	// the last visible values while the controller owns the retry snapshot.
	err := s.storage.write("config.json", settings)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.settings = settings
	s.mu.Unlock()
	return err
}

type pageSettings struct {
	Settings
	IdleMinutes float64 `json:"idle_minutes"`
}

func pageSettingsFor(s Settings) pageSettings {
	d, _ := time.ParseDuration(s.Idle)
	return pageSettings{s, d.Minutes()}
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
