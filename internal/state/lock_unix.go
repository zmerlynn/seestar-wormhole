//go:build darwin || linux

package state

import (
	"errors"
	"os"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"golang.org/x/sys/unix"
)

// lockFile opens or creates the lock file at path and takes an exclusive
// flock on it. Without wait, a held lock fails with ErrLocked; with it,
// lockFile blocks until the lock is free. flock belongs to the open file
// description, so a second open in the same process conflicts too, unlike
// fcntl locks. The lock lasts until the returned file is closed or the
// process exits.
func lockFile(path string, wait bool) (*os.File, error) {
	readonly.Check("state.lockFile")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	rc, err := f.SyscallConn()
	if err == nil {
		how := unix.LOCK_EX
		if !wait {
			how |= unix.LOCK_NB
		}
		var lerr error
		err = rc.Control(func(fd uintptr) {
			lerr = unix.Flock(int(fd), how)
		})
		if err == nil {
			err = lerr
		}
	}
	if err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}
