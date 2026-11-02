package browse

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"testing/fstest"

	"github.com/zmerlynn/seestar-copy/internal/remote"
)

func TestThumbs(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{"X.fit", []string{"X.fit", "X.jpg", "X_thn.jpg"}, "X_thn.jpg"},
		{"X.fit", []string{"X.fit", "X.jpg"}, "X.jpg"},
		{"X.jpg", []string{"X.jpg"}, "X.jpg"},
		{"X_thn.jpg", []string{"X.fit", "X.jpg", "X_thn.jpg"}, "X_thn.jpg"},
		{"X.mp4", []string{"X.mp4", "X_thn.jpg"}, "X_thn.jpg"},
		{"X.fit", []string{"X.JPG", "X.fit", "X_THN.JPG"}, "X_THN.JPG"},
		{"X.fit", []string{"X.Jpg", "X.fit"}, "X.Jpg"},
		{"X.fit", []string{"X.fit"}, ""},
		{"X.fit", []string{"X.fit", "x_thn.jpg", "x.jpg"}, ""},             // the stem compares exactly
		{"X_thn.fit", []string{"X_THN.jpg", "X_thn.fit"}, ""},              // X_THN.jpg is X's thumbnail, not X_thn's
		{"README", []string{"README", "README_thn.jpg"}, "README_thn.jpg"}, // no dot: the whole name is the stem
		{"a.b.fit", []string{"a.b.fit", "a.b_thn.jpg", "a_thn.jpg"}, "a.b_thn.jpg"},
		{"a.b.fit", []string{"a.b.fit", "a.jpg"}, ""},
		{"X.fit", []string{"X.fit", "X_THN.JPG", "X_thn.jpg"}, "X_THN.JPG"}, // the first in listing order
	}
	for _, tt := range tests {
		if got := newThumbs(tt.files).For(tt.name); got != tt.want {
			t.Errorf("thumb of %q among %q = %q, want %q", tt.name, tt.files, got, tt.want)
		}
	}
}

func TestViewKind(t *testing.T) {
	tests := []struct{ name, want string }{
		{"a.jpg", "image"}, {"a.JPG", "image"}, {"a.Jpg", "image"},
		{"a.txt", "text"}, {"a.TXT", "text"}, {"Autorun_Log_2026.txt", "text"},
		{"a.fit", "fits"}, {"a.FIT", "fits"}, {"a.fits", "fits"}, {"a.FiTs", "fits"},
		{"a.jpeg", ""}, {"a.text", ""}, {"a.txt.gz", ""}, {"a.fit.jpg", "image"}, {"a.fitz", ""}, {"a.fts", ""},
		{"a.mp4", ""}, {"a", ""}, {"jpg", ""}, {"txt", ""}, {"fits", ""}, {".txt", "text"},
	}
	for _, tt := range tests {
		if got := viewKind(tt.name); got != tt.want {
			t.Errorf("viewKind(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestContentTypeAndDisposition(t *testing.T) {
	for _, name := range []string{"a.jpg", "a.JPG", "a.Jpg"} {
		if got := contentType(name); got != "image/jpeg" {
			t.Errorf("contentType(%q) = %q", name, got)
		}
		if got := disposition(name, false); got != "inline; filename="+name {
			t.Errorf("disposition(%q, false) = %q", name, got)
		}
		if got := disposition(name, true); got != "attachment; filename="+name {
			t.Errorf("disposition(%q, true) = %q", name, got)
		}
	}
	for _, name := range []string{"a.txt", "a.TXT", "Autorun_Log_1.Txt"} {
		if got := contentType(name); got != "text/plain; charset=utf-8" {
			t.Errorf("contentType(%q) = %q", name, got)
		}
		if got := disposition(name, false); got != "inline; filename="+name {
			t.Errorf("disposition(%q, false) = %q", name, got)
		}
		if got := disposition(name, true); got != "attachment; filename="+name {
			t.Errorf("disposition(%q, true) = %q", name, got)
		}
	}
	for _, name := range []string{"a.jpeg", "a.png", "a.svg", "a.html", "a.fit", "a.FITS", "a.mp4", "a.text", "a.txt.gz", "a", "jpg", "txt", "a.jpg.fit"} {
		if got := contentType(name); got != "application/octet-stream" {
			t.Errorf("contentType(%q) = %q", name, got)
		}
		if got := disposition(name, false); got != "attachment; filename="+name {
			t.Errorf("disposition(%q, false) = %q", name, got)
		}
	}
	tests := []struct{ name, want string }{
		{`a"b\c.jpg`, `inline; filename="a\"b\\c.jpg"`},
		{"M 31.jpg", `inline; filename="M 31.jpg"`},
		{"M31 – Andromède.jpg", "inline; filename*=utf-8''M31%20%E2%80%93%20Androm%C3%A8de.jpg"},
		{"a\x01.jpg", "inline; filename*=utf-8''a%01.jpg"},
	}
	for _, tt := range tests {
		if got := disposition(tt.name, false); got != tt.want {
			t.Errorf("disposition(%q, false) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// openFixture opens a 100-byte file whose byte i is i.
func openFixture(t *testing.T) (remote.File, []byte) {
	t.Helper()
	data := make([]byte, 100)
	for i := range data {
		data[i] = byte(i)
	}
	f, err := remote.NewFake(fstest.MapFS{"f": {Data: data}}).Open(context.Background(), "f")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, data
}

func TestFileReaderSeeks(t *testing.T) {
	f, data := openFixture(t)
	var failed []error
	r := newFileReader(context.Background(), f, int64(len(data)), func(err error) { failed = append(failed, err) })
	next := func(n int) []byte {
		t.Helper()
		b := make([]byte, n)
		if _, err := io.ReadFull(r, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	if got := next(5); !bytes.Equal(got, data[:5]) {
		t.Fatalf("first read = %v", got)
	}
	steps := []struct {
		offset int64
		whence int
		want   int64
	}{
		{3, io.SeekCurrent, 8}, // from the logical offset 5, not the buffered handle's
		{2, io.SeekStart, 2},
		{-1, io.SeekEnd, 99},
	}
	for _, s := range steps {
		got, err := r.Seek(s.offset, s.whence)
		if err != nil || got != s.want {
			t.Fatalf("Seek(%d, %d) = %d, %v; want %d", s.offset, s.whence, got, err, s.want)
		}
		if b := next(1); b[0] != data[s.want] {
			t.Fatalf("after Seek(%d, %d): read %d, want %d", s.offset, s.whence, b[0], data[s.want])
		}
	}
	if _, err := r.Seek(-1, io.SeekStart); err == nil {
		t.Error("Seek to a negative offset succeeded")
	}
	if _, err := r.Seek(0, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	if n, err := r.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Errorf("Read at the end = %d, %v; want 0, EOF", n, err)
	}
	if failed != nil {
		t.Errorf("fail got %v", failed)
	}
}

func TestFileReaderStopsForDepartedClient(t *testing.T) {
	f, data := openFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	var failed []error
	r := newFileReader(ctx, f, int64(len(data)), func(err error) { failed = append(failed, err) })
	cancel()
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Errorf("Read = %v, want context.Canceled", err)
	}
	if failed != nil {
		t.Errorf("the request's own error went to fail: %v", failed)
	}
}

type failingFile struct {
	remote.File
	err error
}

func (f failingFile) Read([]byte) (int, error)       { return 0, f.err }
func (f failingFile) Seek(int64, int) (int64, error) { return 0, f.err }

func TestFileReaderReportsDeviceErrors(t *testing.T) {
	f, data := openFixture(t)
	broken := errors.New("broken pipe")
	var failed []error
	r := newFileReader(context.Background(), failingFile{f, broken}, int64(len(data)), func(err error) { failed = append(failed, err) })
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, broken) {
		t.Errorf("Read = %v", err)
	}
	if _, err := r.Seek(0, io.SeekStart); !errors.Is(err, broken) {
		t.Errorf("Seek = %v", err)
	}
	if len(failed) != 2 {
		t.Errorf("fail got %v, want the Read and Seek errors", failed)
	}
	if err := r.stop(); !errors.Is(err, broken) {
		t.Errorf("stop = %v, want the last device error", err)
	}
	if _, err := r.Read(make([]byte, 1)); err != errReaderStopped {
		t.Errorf("Read after stop = %v", err)
	}
	if _, err := r.Seek(0, io.SeekStart); err != errReaderStopped {
		t.Errorf("Seek after stop = %v", err)
	}
	if len(failed) != 2 {
		t.Errorf("a device call after stop: fail got %v", failed)
	}
}
