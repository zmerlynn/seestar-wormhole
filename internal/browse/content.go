package browse

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"strings"

	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// maxBuffer bounds the read buffer of one file response, so that the 32 KiB
// reads of http.ServeContent do not each become one SMB READ. It is tuned by
// measurement, not a contract.
const maxBuffer = 4 << 20

// hasSuffixFold reports whether name ends in suffix, an ASCII string, in any
// case. A non-ASCII rune is at least two bytes, so it can never fold to the
// same number of ASCII runes.
func hasSuffixFold(name, suffix string) bool {
	return len(name) >= len(suffix) && strings.EqualFold(name[len(name)-len(suffix):], suffix)
}

// viewKind is how the page shows a file when clicked, from its name's
// suffix in any case: "image" for .jpg, "text" for .txt, "fits" for .fit and
// .fits, and "" for any other file, which is only saved.
func viewKind(name string) string {
	switch {
	case hasSuffixFold(name, ".jpg"):
		return "image"
	case hasSuffixFold(name, ".txt"):
		return "text"
	case hasSuffixFold(name, ".fit"), hasSuffixFold(name, ".fits"):
		return "fits"
	}
	return ""
}

// contentType is the fixed type a file is served with. mime.TypeByExtension
// is not used, since it reads the system's tables.
func contentType(name string) string {
	switch viewKind(name) {
	case "image":
		return "image/jpeg"
	case "text":
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// disposition is the Content-Disposition of a file response: inline only for
// a .jpg or .txt that is not being saved. A FITS file stays an attachment:
// the page reads its header with fetch, which ignores the disposition.
// FormatMediaType quotes the name, or encodes it per RFC 2231, so no name can
// break the header.
func disposition(name string, download bool) string {
	kind := "attachment"
	if v := viewKind(name); (v == "image" || v == "text") && !download {
		kind = "inline"
	}
	if v := mime.FormatMediaType(kind, map[string]string{"filename": name}); v != "" {
		return v
	}
	return kind
}

// thumbs finds a preview for each file of one folder.
type thumbs struct {
	thn, jpg map[string]string // stem to the first file name that is that stem plus _thn.jpg or .jpg, in any case
}

// newThumbs indexes the regular file names of one folder, in listing order.
func newThumbs(files []string) thumbs {
	t := thumbs{thn: map[string]string{}, jpg: map[string]string{}}
	add := func(m map[string]string, name, suffix string) {
		if !hasSuffixFold(name, suffix) {
			return
		}
		stem := name[:len(name)-len(suffix)]
		if _, ok := m[stem]; !ok {
			m[stem] = name
		}
	}
	for _, name := range files {
		add(t.thn, name, "_thn.jpg")
		add(t.jpg, name, ".jpg")
	}
	return t
}

// For gives the preview of the file name, or "": for a stem S (the name
// before its last dot), S_thn.jpg, else S.jpg. The stem compares exactly;
// only the suffixes match in any case. A .jpg with no smaller preview is its
// own.
func (t thumbs) For(name string) string {
	stem := name
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		stem = name[:i]
	}
	if v, ok := t.thn[stem]; ok {
		return v
	}
	return t.jpg[stem]
}

// fileReader is the io.ReadSeeker http.ServeContent reads one open file
// through. It keeps its own offset, buffers reads, takes the end from the
// size Stat gave, and starts no device read once the request has ended.
//
// A multipart response reads in a goroutine of ServeContent's that can
// outlive the call, so mu is held through each Read and Seek, and stop
// waits for one in progress and refuses any later one. mu is a channel of
// one slot rather than a sync.Mutex, so testing/synctest sees a wait on it
// as durably blocked.
type fileReader struct {
	mu      chan struct{}
	stopped bool
	err     error // the last device error but EOF
	f       remote.File
	size    int64
	off     int64 // the logical offset
	src     source
	buf     *bufio.Reader
	fail    func(error)
}

// errReaderStopped is what a fileReader answers after stop.
var errReaderStopped = errors.New("file reader stopped")

// source is the device side of a fileReader's buffer.
type source struct {
	ctx context.Context // the request's
	r   *fileReader
}

func (s source) Read(p []byte) (int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := s.r.f.Read(p)
	if err != nil && err != io.EOF {
		s.r.failed(err)
	}
	return n, err
}

// newFileReader reads f, whose Stat gave size, for the request ctx. fail gets
// every device error from a Read or Seek, but not the request's own error.
func newFileReader(ctx context.Context, f remote.File, size int64, fail func(error)) *fileReader {
	r := &fileReader{mu: make(chan struct{}, 1), f: f, size: size, fail: fail}
	r.src = source{ctx: ctx, r: r}
	r.buf = bufio.NewReaderSize(r.src, int(min(size, maxBuffer)))
	return r
}

func (r *fileReader) lock()   { r.mu <- struct{}{} }
func (r *fileReader) unlock() { <-r.mu }

// failed records a device error and passes it on; mu is held.
func (r *fileReader) failed(err error) {
	r.err = err
	r.fail(err)
}

// stop waits for a Read or Seek in progress, refuses any later one, and
// gives the last device error, or nil.
func (r *fileReader) stop() error {
	r.lock()
	defer r.unlock()
	r.stopped = true
	return r.err
}

func (r *fileReader) Read(p []byte) (int, error) {
	r.lock()
	defer r.unlock()
	if r.stopped {
		return 0, errReaderStopped
	}
	n, err := r.buf.Read(p)
	r.off += int64(n)
	return n, err
}

func (r *fileReader) Seek(offset int64, whence int) (int64, error) {
	r.lock()
	defer r.unlock()
	if r.stopped {
		return 0, errReaderStopped
	}
	var to int64
	switch whence {
	case io.SeekStart:
		to = offset
	case io.SeekCurrent:
		to = r.off + offset
	case io.SeekEnd:
		to = r.size + offset
	default:
		return 0, errors.New("seek: invalid whence")
	}
	if to < 0 {
		return 0, errors.New("seek: negative position")
	}
	if _, err := r.f.Seek(to, io.SeekStart); err != nil {
		r.failed(err)
		return 0, err
	}
	r.buf.Reset(r.src)
	r.off = to
	return to, nil
}
