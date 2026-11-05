package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func lock(f *os.File, wait bool) (bool, error) {
	var flags uint32 = windows.LOCKFILE_EXCLUSIVE_LOCK
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	err := control(f, func(h windows.Handle) error {
		return windows.LockFileEx(h, flags, 0, 1, 0, new(windows.Overlapped))
	})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

func unlock(f *os.File) error {
	return control(f, func(h windows.Handle) error {
		return windows.UnlockFileEx(h, 0, 1, 0, new(windows.Overlapped))
	})
}

func control(f *os.File, fn func(windows.Handle) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = fn(windows.Handle(fd)) }); err != nil {
		return err
	}
	return ferr
}
