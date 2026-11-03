package browse

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-copy/internal/remote"
)

const fitPath = "MyWorks/M 31/X.fit"

// saveEnv is a browse env whose sessions inject fault into X.fit, and a
// destination folder holding an existing X.fit.
func saveEnv(t *testing.T, fault *remote.Fault) (*env, string) {
	t.Helper()
	e := newEnv(t, envOptions{setup: func(s *spy) {
		if fault != nil {
			s.fake.Faults = map[string]remote.Fault{fitPath: *fault}
		}
	}})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "X.fit"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	return e, dir
}

// onlyFile checks that dir holds just X.fit, with want in it.
func onlyFile(t *testing.T, dir string, want []byte) {
	t.Helper()
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0].Name() != "X.fit" {
		t.Errorf("folder holds %v, want just X.fit", names)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "X.fit")); !bytes.Equal(got, want) {
		t.Errorf("X.fit holds %d bytes %.10q, want %d", len(got), got, len(want))
	}
}

func TestSave(t *testing.T) {
	e, dir := saveEnv(t, nil)
	if err := Save(t.Context(), e.p, fitPath, filepath.Join(dir, "X.fit")); err != nil {
		t.Fatal(err)
	}
	onlyFile(t, dir, fitData)
	if err := Save(t.Context(), e.p, "MyWorks/M 31/X.jpg", filepath.Join(dir, "new.jpg")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "new.jpg")); string(got) != "jpeg" {
		t.Errorf("new.jpg = %q", got)
	}
}

// TestSaveFailures checks that every failure leaves the existing file as it
// was and no temporary file.
func TestSaveFailures(t *testing.T) {
	lost := errors.New("connection reset")
	tests := []struct {
		name  string
		path  string
		fault *remote.Fault
		ctx   func(context.Context) context.Context
		want  func(error) bool
	}{
		{name: "missing", path: "MyWorks/M 31/gone.fit", want: func(err error) bool { return errors.Is(err, fs.ErrNotExist) }},
		{name: "folder", path: "MyWorks/M 31", want: func(err error) bool { return err.Error() == "it is a folder on the device" }},
		{name: "connection lost", path: fitPath, fault: &remote.Fault{After: 50, ReadErr: lost},
			want: func(err error) bool { return errors.Is(err, lost) }},
		{name: "short", path: fitPath, fault: &remote.Fault{After: 50, ReadErr: io.EOF},
			want: func(err error) bool { return err.Error() == "the device sent 50 of its 200 bytes" }},
		{name: "canceled", path: fitPath, want: func(err error) bool { return errors.Is(err, context.Canceled) },
			ctx: func(ctx context.Context) context.Context {
				ctx, cancel := context.WithCancel(ctx)
				cancel()
				return ctx
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, dir := saveEnv(t, tt.fault)
			ctx := t.Context()
			if tt.ctx != nil {
				// Take the session first, so the canceled context ends the
				// copy rather than the dial.
				mustGet(t, e.p, ctx).Release()
				ctx = tt.ctx(ctx)
			}
			err := Save(ctx, e.p, tt.path, filepath.Join(dir, "X.fit"))
			if err == nil || !tt.want(err) {
				t.Errorf("Save = %v", err)
			}
			onlyFile(t, dir, []byte("old"))
		})
	}
}

// TestSaveCanceledMidFile cancels during the first read; the copy stops
// at the next.
func TestSaveCanceledMidFile(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	e := newEnv(t, envOptions{setup: func(s *spy) { s.readFault = func() error { cancel(); return nil } }})
	dir := t.TempDir()
	err := Save(ctx, e.p, fitPath, filepath.Join(dir, "X.fit"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Save = %v, want canceled", err)
	}
	if names, _ := os.ReadDir(dir); len(names) != 0 {
		t.Errorf("folder holds %v", names)
	}
}

func TestSaveUnwritable(t *testing.T) {
	e, dir := saveEnv(t, nil)
	err := Save(t.Context(), e.p, fitPath, filepath.Join(dir, "missing", "X.fit"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Save = %v", err)
	}
	onlyFile(t, dir, []byte("old"))
}

// TestDownload checks that Download leaves only a checked GET with
// download=1 to its caller, and serves or refuses every other request as
// ServeHTTP does.
func TestDownload(t *testing.T) {
	e := newEnv(t, envOptions{})
	jpg := "/file?path=MyWorks%2FM%2031%2FX.jpg"
	tests := []struct {
		method, target string
		headers        []string
		code           int
		path           string // the path handed back; "" for none
	}{
		{"GET", jpg + "&download=1", nil, 0, "MyWorks/M 31/X.jpg"},
		{"GET", jpg, nil, 200, ""},
		{"GET", jpg + "&download=0", nil, 200, ""},
		{"HEAD", jpg + "&download=1", nil, 200, ""},
		{"POST", jpg + "&download=1", nil, 405, ""},
		{"GET", jpg + "&download=1", []string{"Host", "localhost:53817"}, 403, ""},
		{"GET", jpg + "&download=1", []string{"Sec-Fetch-Site", "cross-site"}, 403, ""},
		{"GET", jpg + "&download=1&download=1", nil, 400, ""},
		{"GET", "/file?path=..%2FX.jpg&download=1", nil, 400, ""},
		{"GET", "/file?path=%2FX.jpg&download=1", nil, 400, ""},
		{"GET", "/api/list?path=MyWorks&download=1", nil, 200, ""},
		{"GET", "/?download=1", nil, 200, ""},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(tt.method, tt.target, nil)
		r.Host = e.addr.String()
		for i := 0; i < len(tt.headers); i += 2 {
			if tt.headers[i] == "Host" {
				r.Host = tt.headers[i+1]
			} else {
				r.Header.Set(tt.headers[i], tt.headers[i+1])
			}
		}
		w := httptest.NewRecorder()
		p, ok := e.h.Download(w, r)
		if p != tt.path || ok != (tt.path != "") {
			t.Errorf("%s %s: Download = %q, %v; want %q", tt.method, tt.target, p, ok, tt.path)
		}
		if tt.code == 0 {
			if w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
				t.Errorf("%s %s: answered %q, %v", tt.method, tt.target, w.Body, w.Header())
			}
			if !strings.HasSuffix(w.Header().Get("Content-Security-Policy"), "; sandbox") {
				t.Errorf("%s %s: headers %v", tt.method, tt.target, w.Header())
			}
		} else if w.Code != tt.code {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.target, w.Code, tt.code)
		}
	}
}
