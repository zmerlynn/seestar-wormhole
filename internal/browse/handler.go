package browse

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"time"
	"unicode/utf8"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

//go:embed web/*
var assets embed.FS

// Config is what the handler serves.
type Config struct {
	Addr     netip.AddrPort                          // the listener's address, with its real port
	Endpoint string                                  // shown in the page header
	Sessions *Sessions                               // the shared SMB session
	Records  func() (map[string]state.Record, error) // a destination's state; nil for no statuses
	Log      *slog.Logger
	// InApp serves the page for the seestar-copy app, which shows it in a
	// frame of its own origin and has its own header: the page may be
	// framed by that origin only, hides its header, and its error hint
	// does not say to reload a page the user cannot see.
	InApp bool
}

const (
	cspStart = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; "
	cspEnd   = "; base-uri 'none'; form-action 'none'"
	csp      = cspStart + "frame-ancestors 'none'" + cspEnd
	cspInApp = cspStart + "frame-ancestors 'self'" + cspEnd
)

// static are the page's files, by route.
var static = map[string]struct{ file, typ string }{
	"/":          {"web/index.html", "text/html; charset=utf-8"},
	"/app.js":    {"web/app.js", "text/javascript; charset=utf-8"},
	"/style.css": {"web/style.css", "text/css; charset=utf-8"},
	"/icon.svg":  {"web/icon.svg", "image/svg+xml"},
}

// Handler is the browse page and its API.
type Handler struct{ cfg Config }

// NewHandler returns the browse page and its API. It answers GET and HEAD
// only, refuses a Host that is not an IP literal for cfg.Addr and any
// cross-site request, and touches neither the state nor the device until a
// request has passed every check.
func NewHandler(cfg Config) *Handler { return &Handler{cfg: cfg} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.serve(w, r, false) }

// Download serves r as ServeHTTP does, except a /file request with
// download=1 that passes every check: that GET it leaves unanswered and
// returns the file's share path and true, so the caller can save the file
// itself. A refused request gets ServeHTTP's answer.
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) (string, bool) {
	return h.serve(w, r, true)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request, intercept bool) (string, bool) {
	hd := w.Header()
	policy, frames := csp, "DENY"
	if h.cfg.InApp {
		policy, frames = cspInApp, "SAMEORIGIN"
	}
	if r.URL.Path == "/file" {
		policy += "; sandbox"
	}
	hd.Set("Content-Security-Policy", policy)
	hd.Set("Cross-Origin-Resource-Policy", "same-origin")
	hd.Set("X-Frame-Options", frames)
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cache-Control", "no-store")
	reject := func(code int) { http.Error(w, "Request rejected", code) }
	if !h.hostAllowed(r.Host) {
		reject(http.StatusForbidden)
		return "", false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		hd.Set("Allow", "GET, HEAD")
		reject(http.StatusMethodNotAllowed)
		return "", false
	}
	asset, isStatic := static[r.URL.Path]
	if !isStatic && r.URL.Path != "/api/list" && r.URL.Path != "/file" {
		http.Error(w, "Not found", http.StatusNotFound)
		return "", false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		reject(http.StatusForbidden)
		return "", false
	}
	if isStatic {
		b, err := assets.ReadFile(asset.file)
		if err != nil {
			http.Error(w, "Not found", http.StatusNotFound)
			return "", false
		}
		if h.cfg.InApp && asset.file == "web/index.html" {
			b = bytes.Replace(b, []byte(`<html lang="en">`), []byte(`<html lang="en" class="in-app">`), 1)
		}
		hd.Set("Content-Type", asset.typ)
		w.Write(b)
		return "", false
	}
	list := r.URL.Path == "/api/list"
	fail := h.fileError
	if list {
		fail = h.listError
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err == nil {
		for _, v := range q {
			if len(v) > 1 {
				err = errors.New("repeated parameter")
			}
		}
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_query", "The request's query is malformed.")
		return "", false
	}
	p := q.Get("path")
	root := list && p == "" // the share root, which only a listing names
	if !utf8.ValidString(p) || !(root || remote.ValidPath(p)) {
		fail(w, http.StatusBadRequest, "bad_path", "The path is not a valid share path.")
		return "", false
	}
	if list {
		dir := p
		if root {
			dir = "."
		}
		h.list(w, r, p, dir)
	} else if download := q.Get("download") == "1"; intercept && download && r.Method == http.MethodGet {
		return p, true
	} else {
		h.file(w, r, p, download)
	}
	return "", false
}

// hostAllowed is the Host check. A Host without a port means port 80. For a
// specific address the Host must be exactly that address; for a wildcard,
// any IP literal with the listener's port. A host name is always refused.
func (h *Handler) hostAllowed(host string) bool {
	if _, err := netip.ParseAddrPort(host); err != nil {
		host += ":80"
	}
	if !h.cfg.Addr.Addr().IsUnspecified() {
		return host == h.cfg.Addr.String()
	}
	ap, err := netip.ParseAddrPort(host)
	return err == nil && ap.Port() == h.cfg.Addr.Port()
}

// The JSON of /api/list.
type listing struct {
	Path     string `json:"path"`
	Endpoint string `json:"endpoint"`
	Statuses bool   `json:"statuses"`
	Entries  []item `json:"entries"`
}

type item struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Size   *int64 `json:"size,omitempty"`
	MTime  string `json:"mtime"`
	Status string `json:"status,omitempty"`
	Thumb  string `json:"thumb,omitempty"`
	View   string `json:"view,omitempty"` // the page's viewer: viewKind
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, shown, dir string) {
	var records map[string]state.Record
	if h.cfg.Records != nil {
		var err error
		if records, err = h.cfg.Records(); err != nil {
			h.cfg.Log.Warn("request failed", "path", shown, "error", err)
			h.listError(w, http.StatusInternalServerError, "state", err.Error())
			return
		}
	}
	lease, err := h.cfg.Sessions.Get(r.Context())
	if err != nil {
		h.deviceError(w, r, shown, err, h.listError)
		return
	}
	defer lease.Release()
	entries, err := lease.Session().ReadDir(lease.Context(), dir)
	if err != nil {
		lease.Fail(err)
		h.deviceError(w, r, shown, err, h.listError)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	var files []string
	for _, e := range entries {
		if e.Kind == remote.Regular {
			files = append(files, path.Base(e.Path))
		}
	}
	t := newThumbs(files)
	out := listing{Path: shown, Endpoint: h.cfg.Endpoint, Statuses: records != nil, Entries: make([]item, 0, len(entries))}
	for _, e := range entries {
		it := item{Path: e.Path, MTime: e.ModTime.UTC().Format(time.RFC3339Nano)}
		switch e.Kind {
		case remote.Dir:
			it.Type = "dir"
		case remote.Regular:
			name := path.Base(e.Path)
			it.Type, it.Size, it.View = "file", &e.Size, viewKind(name)
			if th := t.For(name); th != "" {
				it.Thumb = path.Join(path.Dir(e.Path), th)
			}
			if records != nil {
				rec, ok := records[e.Path]
				it.Status = plan.FileStatus(plan.Entry{Path: e.Path, Size: e.Size, ModTime: e.ModTime, Regular: true},
					plan.Recorded{Size: rec.Size, ModTime: rec.MTime}, ok)
			}
		default:
			continue
		}
		out.Entries = append(out.Entries, it)
	}
	b, err := json.Marshal(out)
	if err != nil {
		h.listError(w, http.StatusInternalServerError, "encode", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func (h *Handler) file(w http.ResponseWriter, r *http.Request, p string, download bool) {
	lease, err := h.cfg.Sessions.Get(r.Context())
	if err != nil {
		h.deviceError(w, r, p, err, h.fileError)
		return
	}
	defer lease.Release()
	f, err := lease.Session().Open(lease.Context(), p)
	if err != nil {
		lease.Fail(err)
		h.deviceError(w, r, p, err, h.fileError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		lease.Fail(err)
		h.deviceError(w, r, p, err, h.fileError)
		return
	}
	if info.IsDir() {
		h.fileError(w, http.StatusBadRequest, "not_a_file", "The path is a folder.")
		return
	}
	rs := newFileReader(r.Context(), f, info.Size(), lease.Fail)
	name := path.Base(p)
	w.Header().Set("Content-Type", contentType(name))
	w.Header().Set("Content-Disposition", disposition(name, download))
	http.ServeContent(w, r, "", info.ModTime(), rs)
	// A multipart response's producer may still be in a Read. Wait for it
	// before the file closes and the lease, with its grace, is released.
	if failed := rs.stop(); failed != nil && r.Context().Err() == nil {
		h.cfg.Log.Warn("request failed", "path", p, "error", failed)
	}
}

// deviceError answers a failed Get or SMB call: 404 for a missing path, 400
// for a path of the wrong kind, and 502 for anything else, which is logged
// unless the client has gone away.
func (h *Handler) deviceError(w http.ResponseWriter, r *http.Request, p string, err error,
	fail func(http.ResponseWriter, int, string, string)) {
	switch {
	case r.Context().Err() != nil:
		return
	case errors.Is(err, fs.ErrNotExist):
		fail(w, http.StatusNotFound, "not_found", "Not found on the device.")
	case errors.Is(err, remote.ErrNotDir):
		fail(w, http.StatusBadRequest, "not_a_folder", "The path is not a folder.")
	default:
		h.cfg.Log.Warn("request failed", "path", p, "error", err)
		fail(w, http.StatusBadGateway, "device", err.Error())
	}
}

// listError answers an /api/list request with a JSON error.
func (h *Handler) listError(w http.ResponseWriter, status int, code, message string) {
	b, _ := json.Marshal(struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}{code, message})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

// fileError answers a /file request with plain text.
func (h *Handler) fileError(w http.ResponseWriter, status int, _, message string) {
	http.Error(w, message, status)
}
