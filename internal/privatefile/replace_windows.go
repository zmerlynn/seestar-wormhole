package privatefile

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"syscall"
	"unsafe"
)

var replaceFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReplaceFileW")

func Replace(from, to string) error {
	a, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	b, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	// ReplaceFile permits readers of the old file to retain their handles.
	// MoveFileEx replacement can fail with access denied while such a reader is open.
	ok, _, err := replaceFile.Call(uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(a)), 0, 0, 0, 0)
	if ok != 0 {
		return nil
	}
	if !errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
		return err
	}
	// First publication has no destination to replace. Never delete it first.
	return windows.MoveFileEx(a, b, windows.MOVEFILE_WRITE_THROUGH)
}
func SyncDirectory(*os.File) error { return nil }

// create makes a new file for writing, refusing any existing entry. Under
// Inherited it is an ordinary file that takes its folder's ACL, as
// [os.CreateTemp] makes one, so a filesystem without ACLs works too.
func create(path string, p Policy) (*os.File, error) {
	if p == Inherited {
		return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	return Open(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
}

// publish places a file WriteFile wrote: Private with Replace, Inherited with
// os.Rename, which keeps the ACL the file was created with.
func publish(from, to string, p Policy) error {
	if p == Inherited {
		return os.Rename(from, to)
	}
	return Replace(from, to)
}

// OpenRead pins a validated file handle while permitting atomic replacement of
// its directory entry. The caller pins the private parent directory separately.
// Identity/lock writers continue to use Open without delete sharing.
func OpenRead(path string) (*os.File, error) {
	return openRead(path, false)
}

// ReadDirectory pins and checks an existing private directory without creating it.
func ReadDirectory(path string) (*os.File, error) {
	return openRead(path, true)
}

func openRead(path string, directory bool) (*os.File, error) {
	if err := ValidatePath(path); err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		share &^= windows.FILE_SHARE_DELETE
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.READ_CONTROL, share, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return nil, err
	}
	if err := Check(h, directory); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}
