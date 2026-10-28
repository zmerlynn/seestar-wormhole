package ui

import (
	"crypto/subtle"
	"embed"
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/assets/icon"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed web/*
var assets embed.FS

type server struct {
	host, token   string
	controller    *controller
	storage       *storage
	mu            sync.Mutex
	settings      Settings
	quit          chan struct{}
	quitOnce      sync.Once
	activate      func()
	native        bool
	storageFailed bool
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

		events, path, logFailed := s.controller.log.snapshot()
		s.mu.Lock()
		storageFailed := s.storageFailed
		s.mu.Unlock()
		s.respond(w, struct {
			Settings pageSettings `json:"settings"`
			runView
			Password      passwordInfo `json:"password"`
			Native        bool         `json:"native"`
			Events        []string     `json:"events"`
			LogPath       string       `json:"log_path"`
			LogFailed     bool         `json:"log_failed"`
			StorageFailed bool         `json:"storage_failed"`
		}{pageSettingsFor(settings), s.controller.view(), s.controller.credentials.state(settings.Email), s.native, events, path, logFailed, storageFailed})
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
		fail(http.StatusBadRequest)
		return
	}
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
	case "/api/stop":
		s.controller.stop()

	case "/api/password/save":
		if _, _, err = in.Settings.validate(); err == nil {
			err = s.controller.credentials.remember(r.Context(), in.Settings, in.Password)
			if err == nil {
				err = s.saveSettings(s.controller.credentials.association(in.Settings))
			}
			_, notice := s.controller.credentials.info()
			s.controller.record(notice)
		}
	case "/api/forget":
		err = s.controller.credentials.forget(r.Context(), in.Settings.Email)
		if err == nil {
			err = s.saveSettings(s.controller.credentials.association(in.Settings))
		}
		_, notice := s.controller.credentials.info()
		s.controller.record(notice)
	case "/api/save":
		// Contextual retry only; never accepts arbitrary settings or writes a secret.
		err = s.controller.retrySave()
	case "/api/retry-storage":
		s.mu.Lock()
		retry := s.settings
		failed := s.storageFailed
		s.mu.Unlock()
		if !failed {
			err = &ActionError{Code: "settings"}
		} else {
			err = s.saveSettings(retry)
		}
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		s.respond(w, struct {
			*ActionError
			Message string `json:"message"`
		}{actionError(err), errorCopy(actionError(err).Code)})
		return
	}
	s.respond(w, struct {
		OK bool `json:"ok"`
	}{true})
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
	s.mu.Lock()
	defer s.mu.Unlock()
	// Retain the exact snapshot even on storage failure so retry is truthful.
	s.settings = settings
	err := s.storage.write("config.json", settings)
	s.storageFailed = err != nil
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
