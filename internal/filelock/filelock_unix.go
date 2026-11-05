//go:build unix

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lock(f *os.File, wait bool) (bool, error) {
	how := unix.LOCK_EX
	if !wait {
		how |= unix.LOCK_NB
	}
	err := flock(f, how)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

func unlock(f *os.File) error {
	return flock(f, unix.LOCK_UN)
}

func flock(f *os.File, how int) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = unix.Flock(int(fd), how) }); err != nil {
		return err
	}
	return ferr
}
