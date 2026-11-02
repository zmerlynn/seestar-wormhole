package remote

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
)

// FakeDir is a Fake over a local folder that is also a Remover, for tests.
// It removes through an os.Root, so a folder that is not empty is refused
// as the share refuses it. Its remove methods follow SMBRemover's order:
// readonly.Check, ValidPath, the Fault's BeforeRecheck, a fresh stat, then
// the removal.
type FakeDir struct {
	*Fake
	root *os.Root
	// calls counts RemoveFile and RemoveDir calls, and removes the
	// removals they reached.
	calls, removes atomic.Int64
}

var _ Remover = (*FakeDir)(nil)

// NewFakeDir returns a FakeDir over dir. Close closes it.
func NewFakeDir(dir string) (*FakeDir, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &FakeDir{Fake: NewFake(root.FS()), root: root}, nil
}

// RemoveFile removes a file; see Remover.
func (f *FakeDir) RemoveFile(ctx context.Context, e Entry) error {
	readonly.Check("remote.RemoveFile")
	f.calls.Add(1)
	if !ValidPath(e.Path) {
		return &fs.PathError{Op: "remove", Path: e.Path, Err: fs.ErrInvalid}
	}
	fi, err := f.recheck(ctx, e.Path)
	if err != nil {
		return err
	}
	if !sameFile(fi, e) {
		return &fs.PathError{Op: "remove", Path: e.Path, Err: ErrChanged}
	}
	f.removes.Add(1)
	return f.root.Remove(filepath.FromSlash(e.Path))
}

// RemoveDir removes an empty folder; see Remover.
func (f *FakeDir) RemoveDir(ctx context.Context, p string) error {
	readonly.Check("remote.RemoveDir")
	f.calls.Add(1)
	if !ValidPath(p) {
		return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrInvalid}
	}
	fi, err := f.recheck(ctx, p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return &fs.PathError{Op: "remove", Path: p, Err: ErrChanged}
	}
	f.removes.Add(1)
	err = f.root.Remove(filepath.FromSlash(p))
	// fs.ErrExist matches ENOTEMPTY and EEXIST on Unix and
	// ERROR_DIR_NOT_EMPTY on Windows.
	if errors.Is(err, fs.ErrExist) {
		return &fs.PathError{Op: "remove", Path: p, Err: ErrNotEmpty}
	}
	return err
}

// recheck runs p's BeforeRecheck hook, then stats p, unless the session is
// closed or ctx is done.
func (f *FakeDir) recheck(ctx context.Context, p string) (fs.FileInfo, error) {
	if hook := f.Faults[p].BeforeRecheck; hook != nil {
		hook()
	}
	if f.closed() {
		return nil, &fs.PathError{Op: "remove", Path: p, Err: fs.ErrClosed}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.root.Lstat(filepath.FromSlash(p))
}

// Counts returns how many RemoveFile and RemoveDir calls there were, and
// how many of them reached the removal.
func (f *FakeDir) Counts() (calls, removes int) {
	return int(f.calls.Load()), int(f.removes.Load())
}

// Close ends the session and closes the folder.
func (f *FakeDir) Close() error {
	f.Fake.Close()
	return f.root.Close()
}
