//go:build windows

package pull

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"os"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"golang.org/x/sys/windows"
)

func privateDirectory(path string) (*os.File, error) { return privatefile.Directory(path) }
func checkPrivate(f *os.File) error                  { return privatefile.Check(windows.Handle(f.Fd()), false) }
func checkReparse(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(name)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return failure(diagnostic.UnsafePath)
	}
	return nil
}
func lockFile(f *os.File) error {
	var lock windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &lock)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrLocked
	}
	return err
}
func syncDirectory(string) error { return nil } // Windows file Sync flushes content; no Unix directory fsync.
