package state

import (
	"errors"
	"os"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"golang.org/x/sys/windows"
)

// lockFile opens or creates the lock file at path and takes an exclusive
// LockFileEx lock on its first byte. Without wait, a held lock fails with
// ErrLocked; with it, lockFile blocks until the lock is free, since os opens
// files for synchronous I/O. The lock belongs to the handle, so a second open
// in the same process conflicts too. It lasts until the returned file is
// closed or the process exits.
func lockFile(path string, wait bool) (*os.File, error) {
	readonly.Check("state.lockFile")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	rc, err := f.SyscallConn()
	if err == nil {
		var flags uint32 = windows.LOCKFILE_EXCLUSIVE_LOCK
		if !wait {
			flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
		}
		var lerr error
		err = rc.Control(func(fd uintptr) {
			lerr = windows.LockFileEx(windows.Handle(fd), flags, 0, 1, 0, new(windows.Overlapped))
		})
		if err == nil {
			err = lerr
		}
	}
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}
