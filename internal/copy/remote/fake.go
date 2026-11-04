package remote

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sync"
)

// Fake is a Session backed by an io/fs.FS, for tests. It lists with the same
// code as SMB. Set Faults before the first call; the Fake is then safe for
// concurrent use.
type Fake struct {
	fsys fs.FS
	// Faults makes Open or reads of the named paths fail.
	Faults    map[string]Fault
	done      chan struct{} // closed by Close
	closeOnce sync.Once
}

// Fault is a failure Fake injects into one path.
type Fault struct {
	// OpenErr, if not nil, is returned by Open.
	OpenErr error
	// After is how many bytes reads deliver before ReadErr or Stall applies.
	After int64
	// ReadErr, if not nil, is returned by the read that reaches After.
	// io.EOF makes the file shorter than its listed size.
	ReadErr error
	// Stall, if not nil, is called by the read that reaches After. That
	// read then waits for the Open context to be done and returns its
	// error, or for the session to close and returns fs.ErrClosed, so a
	// test can cancel or close at a known point mid-file.
	Stall func()
	// BeforeRecheck, if not nil, is called by FakeDir's RemoveFile or
	// RemoveDir of the path after the path check and before the fresh
	// stat, so a test can change the path just before it is checked.
	BeforeRecheck func()
}

var _ Session = (*Fake)(nil)

// NewFake returns a Session that serves fsys, such as an fstest.MapFS.
func NewFake(fsys fs.FS) *Fake {
	return &Fake{fsys: fsys, done: make(chan struct{})}
}

// Walk lists the share; see Session.
func (f *Fake) Walk(ctx context.Context, skip func(string, bool) bool, kept func(Entry)) ([]Entry, error) {
	if f.closed() {
		return nil, &fs.PathError{Op: "walk", Path: ".", Err: fs.ErrClosed}
	}
	return walk(ctx, f.fsys, skip, kept)
}

// ReadDir lists one folder; see Session. A path that is not a folder is
// found by a stat first, since an fstest.MapFS does not say so itself.
func (f *Fake) ReadDir(ctx context.Context, dir string) ([]Entry, error) {
	if !validDir(dir) {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: fs.ErrInvalid}
	}
	if f.closed() {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: fs.ErrClosed}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fi, err := fs.Stat(f.fsys, dir); err == nil && !fi.IsDir() {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: ErrNotDir}
	}
	return readDir(f.fsys, dir)
}

// Open opens path for reading; see Session.
func (f *Fake) Open(ctx context.Context, path string) (File, error) {
	if !ValidPath(path) {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
	}
	if f.closed() {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrClosed}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fault, faulty := f.Faults[path]
	if fault.OpenErr != nil {
		return nil, fault.OpenErr
	}
	file, err := f.fsys.Open(path)
	if err != nil {
		return nil, err
	}
	return &fakeFile{session: f, ctx: ctx, file: file, fault: fault, faulty: faulty && (fault.ReadErr != nil || fault.Stall != nil)}, nil
}

// Close ends the session; later calls fail and a stalled read returns
// fs.ErrClosed. It is safe to call more than once.
func (f *Fake) Close() error {
	f.closeOnce.Do(func() { close(f.done) })
	return nil
}

func (f *Fake) closed() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

type fakeFile struct {
	session *Fake
	ctx     context.Context
	file    fs.File
	fault   Fault
	faulty  bool
	read    int64
}

func (f *fakeFile) Read(p []byte) (int, error) {
	if f.session.closed() {
		return 0, fs.ErrClosed
	}
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	if f.faulty {
		left := f.fault.After - f.read
		if left <= 0 {
			if f.fault.Stall != nil {
				f.fault.Stall()
				select {
				case <-f.ctx.Done():
					return 0, f.ctx.Err()
				case <-f.session.done:
					return 0, fs.ErrClosed
				}
			}
			return 0, f.fault.ReadErr
		}
		if int64(len(p)) > left {
			p = p[:left]
		}
	}
	n, err := f.file.Read(p)
	f.read += int64(n)
	return n, err
}

// WriteTo copies through Read, so faults and cancellation apply.
func (f *fakeFile) WriteTo(w io.Writer) (int64, error) {
	return io.Copy(w, struct{ io.Reader }{f})
}

// Seek moves the offset of the underlying file, which must be an
// io.Seeker, and the fault counter with it, so After stays a byte offset.
func (f *fakeFile) Seek(offset int64, whence int) (int64, error) {
	if f.session.closed() {
		return 0, fs.ErrClosed
	}
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	s, ok := f.file.(io.Seeker)
	if !ok {
		return 0, errors.ErrUnsupported
	}
	n, err := s.Seek(offset, whence)
	if err == nil {
		f.read = n
	}
	return n, err
}

// Stat returns the underlying file's information.
func (f *fakeFile) Stat() (fs.FileInfo, error) { return f.file.Stat() }

func (f *fakeFile) Close() error { return f.file.Close() }
