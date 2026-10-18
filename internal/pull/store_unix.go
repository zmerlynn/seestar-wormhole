//go:build !windows

package pull

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"os"
	"syscall"
)

func privateDirectory(path string) (*os.File, error) {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := checkLocalPath(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		f.Close()
		return nil, failure(diagnostic.UnsafePath)
	}
	if info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, failure(diagnostic.MetadataPermissions)
	}
	return f, nil
}
func checkPrivate(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return failure(diagnostic.UnsafePath)
	}
	if info.Mode().Perm()&0077 != 0 {
		return failure(diagnostic.MetadataPermissions)
	}
	return nil
}
func checkReparse(string) error { return nil }
func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
