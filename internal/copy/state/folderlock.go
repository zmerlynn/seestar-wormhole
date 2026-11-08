package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/filelock"
)

// ErrFolderLocked reports that another writer holds a destination's folder
// lock: a sync pass, a delete or a save into it.
var ErrFolderLocked = errors.New("another copy or delete is using this folder")

// ErrReadOnlyFS reports that the folder lock could not be created because
// the destination is on a read-only file system.
var ErrReadOnlyFS = errors.New("the folder is on a read-only file system")

// LockFolder takes the folder lock of the destination open as root: an
// exclusive, non-blocking [filelock] lock on the file [plan.LockName] at
// the top of it, created 0600 if missing and never removed. Taking it
// through the root that the writer then writes through means the lock
// guards the folder actually written, however the folder was spelled,
// renamed or replaced.
//
// The lock file must be a regular file or absent. A held lock fails with an
// error wrapping [ErrFolderLocked]; a read-only file system with one
// wrapping [ErrReadOnlyFS]; a folder that was removed after root was opened
// with one wrapping [fs.ErrNotExist]. Every error names the destination and
// the lock file. The lock lasts until the returned file is closed or the
// process exits.
func LockFolder(root *os.Root) (*os.File, error) {
	readonly.Check("state.LockFolder")
	dest := root.Name()
	lockPath := filepath.Join(dest, plan.LockName)
	fail := func(err error) error {
		return fmt.Errorf("destination %s: lock file %s: %w", dest, lockPath, err)
	}
	if fi, err := root.Lstat(plan.LockName); err == nil && !fi.Mode().IsRegular() {
		return nil, fail(fmt.Errorf("not a regular file (%s); remove it", fi.Mode().Type()))
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fail(err)
	}
	f, err := root.OpenFile(plan.LockName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		if readOnlyFS(err) {
			err = fmt.Errorf("%w: %w", ErrReadOnlyFS, err)
		}
		return nil, fail(err)
	}
	locked, err := filelock.TryLock(f)
	switch {
	case err != nil:
		f.Close()
		return nil, fail(err)
	case !locked:
		f.Close()
		return nil, fmt.Errorf("destination %s: %w (lock file %s)", dest, ErrFolderLocked, lockPath)
	}
	return f, nil
}
