package privatefile

import (
	"golang.org/x/sys/windows"
	"os"
)

func Replace(from, to string) error {
	a, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	b, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(a, b, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func SyncDirectory(*os.File) error { return nil }

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
