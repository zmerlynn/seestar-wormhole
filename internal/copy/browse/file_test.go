package browse

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"testing"
	"testing/fstest"
	"time"
)

const (
	fitURL = "/file?path=MyWorks%2FM%2031%2FX.fit"
	txtURL = "/file?path=MyWorks%2Flog%2FAutorun_Log_1.txt"
)

func TestFile(t *testing.T) {
	e := newEnv(t, envOptions{})
	tests := []struct {
		target    string
		typ, disp string
	}{
		{fitURL, "application/octet-stream", "attachment; filename=X.fit"},
		{fitURL + "&download=1", "application/octet-stream", "attachment; filename=X.fit"},
		{"/file?path=MyWorks%2FM%2031%2FX.jpg", "image/jpeg", "inline; filename=X.jpg"},
		{"/file?path=MyWorks%2FM%2031%2FX.jpg&download=1", "image/jpeg", "attachment; filename=X.jpg"},
		{"/file?download=1&path=MyWorks%2FM%2031%2FX.jpg", "image/jpeg", "attachment; filename=X.jpg"},
		{"/file?path=MyWorks%2FM%2031%2FX.jpg&download=yes", "image/jpeg", "inline; filename=X.jpg"},
		{txtURL, "text/plain; charset=utf-8", "inline; filename=Autorun_Log_1.txt"},
		{txtURL + "&download=1", "text/plain; charset=utf-8", "attachment; filename=Autorun_Log_1.txt"},
		{"/file?path=MyWorks%2Flog%2Fb.FITS", "application/octet-stream", "attachment; filename=b.FITS"},
	}
	for _, tt := range tests {
		w := e.serve("GET", tt.target)
		h := w.Header()
		if w.Code != 200 || h.Get("Content-Type") != tt.typ || h.Get("Content-Disposition") != tt.disp {
			t.Errorf("%s = %d, %q, %q; want 200, %q, %q", tt.target, w.Code, h.Get("Content-Type"), h.Get("Content-Disposition"), tt.typ, tt.disp)
		}
		if h.Get("Last-Modified") != t0.Format(http.TimeFormat) {
			t.Errorf("%s: Last-Modified %q", tt.target, h.Get("Last-Modified"))
		}
	}
	w := e.serve("GET", fitURL)
	if !bytes.Equal(w.Body.Bytes(), fitData) || w.Header().Get("Content-Length") != "200" {
		t.Errorf("full body: %d bytes, Content-Length %q", w.Body.Len(), w.Header().Get("Content-Length"))
	}
	if w := e.serve("GET", txtURL); w.Body.String() != logText || w.Header().Get("Content-Security-Policy") != csp+"; sandbox" ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("text file: %q, headers %v", w.Body, w.Header())
	}
	if w := e.serve("GET", txtURL, "Range", "bytes=0-1048575"); w.Code != http.StatusPartialContent || w.Body.String() != logText ||
		w.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-%d/%d", len(logText)-1, len(logText)) {
		t.Errorf("text file's first MiB = %d %q, Content-Range %q", w.Code, w.Body, w.Header().Get("Content-Range"))
	}
	ranges := []struct {
		header string
		want   []byte
	}{
		{"bytes=0-9", fitData[0:10]},
		{"bytes=-10", fitData[190:]},
		{"bytes=100-", fitData[100:]},
		{"bytes=0-9", fitData[0:10]}, // the same range again, on the same handler
		{"bytes=150-159", fitData[150:160]},
	}
	for _, r := range ranges {
		w := e.serve("GET", fitURL, "Range", r.header)
		if w.Code != http.StatusPartialContent || !bytes.Equal(w.Body.Bytes(), r.want) {
			t.Errorf("Range %s = %d, %v", r.header, w.Code, w.Body.Bytes())
		}
	}

	w = e.serve("GET", fitURL, "Range", "bytes=0-9,20-29")
	_, params, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
	if w.Code != http.StatusPartialContent || err != nil {
		t.Fatalf("multipart range = %d, %q", w.Code, w.Header().Get("Content-Type"))
	}
	mr := multipart.NewReader(w.Body, params["boundary"])
	for _, want := range [][]byte{fitData[0:10], fitData[20:30]} {
		part, err := mr.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(part)
		if !bytes.Equal(got, want) || part.Header.Get("Content-Type") != "application/octet-stream" {
			t.Errorf("part = %v %q, want %v", got, part.Header.Get("Content-Type"), want)
		}
	}
	if _, err := mr.NextPart(); err != io.EOF {
		t.Errorf("a third part: %v", err)
	}

	if w := e.serve("GET", fitURL, "Range", "bytes=500-"); w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("unsatisfiable range = %d", w.Code)
	}
	old := t0.Add(-time.Hour).Format(http.TimeFormat)
	if w := e.serve("GET", fitURL, "Range", "bytes=0-9", "If-Range", old); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), fitData) {
		t.Errorf("If-Range with an old date = %d, %d bytes; want 200 and the whole file", w.Code, w.Body.Len())
	}
	if w := e.serve("GET", fitURL, "If-Modified-Since", t0.Format(http.TimeFormat)); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Errorf("If-Modified-Since the mtime = %d", w.Code)
	}

	s := e.d.session(0)
	reads := s.n("Read")
	w = e.serve("HEAD", fitURL)
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "200" || s.n("Read") != reads {
		t.Errorf("HEAD = %d, %d bytes, Content-Length %q, %d reads", w.Code, w.Body.Len(), w.Header().Get("Content-Length"), s.n("Read")-reads)
	}

	if w := e.serve("GET", "/file?path=MyWorks"); w.Code != 400 || w.Body.String() != "The path is a folder.\n" {
		t.Errorf("a folder = %d %q", w.Code, w.Body)
	}
	if w := e.serve("GET", "/file?path=MyWorks%2Fnothing.fit"); w.Code != 404 {
		t.Errorf("a missing file = %d %q", w.Code, w.Body)
	}
	if s.n("Open") != s.n("FileClose")+1 { // every open but the missing file's
		t.Errorf("%d opens, %d closes", s.n("Open"), s.n("FileClose"))
	}
	if e.d.dials() != 1 || s.closed() || e.logs.count("request failed") != 0 {
		t.Errorf("%d dials, closed %v; logs:\n%s", e.d.dials(), s.closed(), e.logs)
	}
}

// The page asks every text or FITS read for its first bytes by range, even
// when the listing said the file was empty. An empty file is answered 200
// with no body, not 416, which the page would show as an error.
func TestFileEmptyRange(t *testing.T) {
	e := newEnv(t, envOptions{extra: fstest.MapFS{"MyWorks/log/empty.txt": {ModTime: t0}}})
	w := e.serve("GET", "/file?path=MyWorks%2Flog%2Fempty.txt", "Range", "bytes=0-1048575")
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Errorf("empty file's first MiB = %d, %d bytes; want 200 and none", w.Code, w.Body.Len())
	}
}

// A Seek fault reaches Fail and retires the session; the answer is
// http.ServeContent's own.
func TestFileSeekFault(t *testing.T) {
	fault := errors.New("seek fault")
	tests := []struct {
		name string
		at   func(offset int64) bool
		rng  string
		code int
	}{
		{"size or reset seek", func(int64) bool { return true }, "", http.StatusInternalServerError},
		{"range seek", func(o int64) bool { return o == 10 }, "bytes=10-19", http.StatusRequestedRangeNotSatisfiable},
	}
	for _, tt := range tests {
		first := true
		e := newEnv(t, envOptions{setup: func(s *spy) {
			if first {
				s.seekFault = func(o int64, _ int) error {
					if tt.at(o) {
						return fault
					}
					return nil
				}
			}
			first = false
		}})
		var headers []string
		if tt.rng != "" {
			headers = []string{"Range", tt.rng}
		}
		if w := e.serve("GET", fitURL, headers...); w.Code != tt.code {
			t.Errorf("%s: %d %q, want %d", tt.name, w.Code, w.Body, tt.code)
		}
		if !e.d.session(0).closed() || e.d.session(0).n("FileClose") != 1 {
			t.Errorf("%s: the session was kept", tt.name)
		}
		if e.logs.count("request failed", "seek fault") != 1 || e.logs.count("session closed", "reason=failed") != 1 {
			t.Errorf("%s: logs:\n%s", tt.name, e.logs)
		}
		if w := e.serve("GET", fitURL); w.Code != 200 || e.d.dials() != 2 {
			t.Errorf("%s: next request = %d after %d dials", tt.name, w.Code, e.d.dials())
		}
	}
}
