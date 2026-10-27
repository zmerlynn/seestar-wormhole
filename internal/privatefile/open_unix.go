//go:build unix

// Package privatefile opens files with platform-enforced private access.
package privatefile

import (
	"errors"
	"os"
	"syscall"
)

func ValidatePath(string) error { return nil }
func checkUnix(f *os.File, directory bool) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if directory {
		mode = 0700
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) || info.Mode().Perm() != mode {
		return errors.New("private file: unsafe permissions or type")
	}
	return nil
}
func Open(path string, flags int) (*os.File, error) {
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	if err := checkUnix(f, false); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func Directory(path string) (*os.File, error) {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if err := checkUnix(f, true); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func Replace(from, to string) error  { return os.Rename(from, to) }
func SyncDirectory(f *os.File) error { return f.Sync() }

// OpenRead allows an atomic-file reader to retain the old inode across replacement.
func OpenRead(path string) (*os.File, error) { return Open(path, os.O_RDONLY) }
