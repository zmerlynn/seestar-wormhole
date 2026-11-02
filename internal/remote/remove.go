package remote

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/hirochachacha/go-smb2/v2/x/protocol"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
)

// Remover is a Session that can also remove a regular file or an empty
// folder. Only the delete command dials one.
//
// Both remove methods call readonly.Check first, then refuse a path that
// ValidPath refuses with fs.ErrInvalid, before anything reaches the share.
// They then stat the path and remove it by path. Nothing ties the object
// that was checked to the object that is removed: something written at the
// path between the two is removed too.
type Remover interface {
	Session
	// RemoveFile removes e.Path if it is still a regular file with e's size
	// and mtime; otherwise it returns ErrChanged. A path that is gone
	// returns an error matching fs.ErrNotExist.
	RemoveFile(ctx context.Context, e Entry) error
	// RemoveDir removes the folder p if it is still a folder and empty. A
	// folder with anything in it returns ErrNotEmpty, anything else at p
	// ErrChanged, and a path that is gone an error matching fs.ErrNotExist.
	RemoveDir(ctx context.Context, p string) error
}

// Errors the remove methods return, wrapped in an *fs.PathError.
var (
	// ErrChanged is returned for a path that is no longer what was listed.
	ErrChanged = errors.New("changed since it was listed")
	// ErrNotEmpty is returned for a folder that is not empty.
	ErrNotEmpty = errors.New("folder is not empty")
)

// statusDirectoryNotEmpty is STATUS_DIRECTORY_NOT_EMPTY ([MS-ERREF] 2.3.1).
const statusDirectoryNotEmpty = 0xC0000101

// SMBRemover is a Remover on the telescope's share, through go-smb2.
type SMBRemover struct {
	*SMB
}

var _ Remover = (*SMBRemover)(nil)

// DialRemover is Dial for a session that can remove. Only the delete
// command calls it.
func DialRemover(ctx context.Context, endpoint string) (*SMBRemover, error) {
	s, err := Dial(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return &SMBRemover{s}, nil
}

// RemoveFile removes a file; see Remover. go-smb2's Remove opens the path
// for DELETE, marks it for deletion and closes it. If the server refuses
// because the file is read-only, go-smb2 clears its read-only attribute by
// path and tries once more; the attribute stays cleared if that fails.
func (r *SMBRemover) RemoveFile(ctx context.Context, e Entry) error {
	readonly.Check("remote.RemoveFile")
	if !ValidPath(e.Path) {
		return &fs.PathError{Op: "remove", Path: e.Path, Err: fs.ErrInvalid}
	}
	fi, err := r.share.Lstat(ctx, e.Path)
	if err != nil {
		return fmt.Errorf("stat %q on %s: %w", e.Path, r.endpoint, err)
	}
	if !sameFile(fi, e) {
		return &fs.PathError{Op: "remove", Path: e.Path, Err: ErrChanged}
	}
	if err := r.share.Remove(ctx, e.Path); err != nil {
		return fmt.Errorf("remove %q on %s: %w", e.Path, r.endpoint, err)
	}
	return nil
}

// RemoveDir removes an empty folder; see Remover. The server refuses a
// folder that is not empty, so what is added to it after the listing keeps
// it. The read-only attribute is handled as in RemoveFile.
func (r *SMBRemover) RemoveDir(ctx context.Context, p string) error {
	readonly.Check("remote.RemoveDir")
	if !ValidPath(p) {
		return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrInvalid}
	}
	fi, err := r.share.Lstat(ctx, p)
	if err != nil {
		return fmt.Errorf("stat %q on %s: %w", p, r.endpoint, err)
	}
	if !fi.IsDir() {
		return &fs.PathError{Op: "remove", Path: p, Err: ErrChanged}
	}
	if err := r.share.Remove(ctx, p); err != nil {
		if rerr, ok := errors.AsType[*protocol.ResponseError](err); ok && rerr.Code == statusDirectoryNotEmpty {
			return &fs.PathError{Op: "remove", Path: p, Err: ErrNotEmpty}
		}
		return fmt.Errorf("remove %q on %s: %w", p, r.endpoint, err)
	}
	return nil
}

// sameFile reports whether fi, a fresh stat, is a regular file with the
// size and mtime e was listed with.
func sameFile(fi fs.FileInfo, e Entry) bool {
	return fi.Mode().IsRegular() && fi.Size() == e.Size && fi.ModTime().Equal(e.ModTime)
}
