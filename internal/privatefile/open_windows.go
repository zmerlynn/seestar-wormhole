//go:build windows

// Package privatefile opens files with platform-enforced private access.
package privatefile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FILE_ALL_ACCESS from winnt.h; x/sys does not export the aggregate.
const fileAllAccess windows.ACCESS_MASK = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

// security uses the OS's SDDL codec, with no inherited access. Only the current
// user and SYSTEM receive access. Filesystems without DACL support fail closed.
func security() (*windows.SecurityAttributes, *windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	dacl := "D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)"
	if user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		dacl = "D:P(A;;FA;;;SY)"
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + dacl)
	if err != nil {
		return nil, nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, sd, nil
}

// Check refuses reparse points, unexpected types, foreign owners and any DACL
// except a protected full-access DACL for the current user and SYSTEM. SDDL
// decoding and ACL/ACE retrieval use Windows APIs, never a handwritten parser.
func Check(h windows.Handle, directory bool) error {
	kind, err := windows.GetFileType(h)
	if err != nil {
		return err
	}
	if kind != windows.FILE_TYPE_DISK {
		return errors.New("private file: not a disk file")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return errors.New("private file: unsafe type")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	actual, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := actual.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !windows.EqualSid(owner, user.User.Sid) {
		return errors.New("private file: unsafe owner")
	}
	control, _, err := actual.Control()
	if err != nil {
		return err
	}
	acl, _, err := actual.DACL()
	if err != nil {
		return err
	}
	count := uint16(2)
	if user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		count = 1
	}
	if control&windows.SE_DACL_PROTECTED == 0 || acl == nil || acl.AceCount != count {
		return errors.New("private file: unsafe access")
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	seenUser, seenSystem := false, false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || ace.Mask != fileAllAccess {
			return errors.New("private file: unsafe access")
		}
		// GetAce supplies the native ACCESS_ALLOWED_ACE. SidStart is the first
		// field of its variable-length SID, as defined by the Windows ABI.
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if windows.EqualSid(sid, user.User.Sid) {
			seenUser = true
			if windows.EqualSid(sid, system) {
				seenSystem = true
			}
		} else if windows.EqualSid(sid, system) {
			seenSystem = true
		} else {
			return errors.New("private file: unsafe access")
		}
	}
	if !seenUser || !seenSystem {
		return errors.New("private file: unsafe access")
	}

	return nil
}

// ValidatePath rejects stream aliases before any native creation or ACL change.
// Only a drive prefix may contain a colon; UNC shares are not drive prefixes.
func ValidatePath(path string) error {
	volume := filepath.VolumeName(path)
	drive := strings.TrimPrefix(volume, `\\?\`)
	if len(drive) == 2 && drive[1] == ':' {
		path = path[len(volume):]
	}
	if strings.Contains(path, ":") {
		return errors.New("private file: alternate data streams unsupported")
	}
	return nil
}

// Open refuses final-component reparse points before any read/write. CREATE_NEW
// also refuses existing paths for captures. WRITE_THROUGH plus Sync flushes file
// content/metadata; Windows has no unprivileged Unix directory-fsync equivalent.
func Open(path string, flags int) (*os.File, error) {
	if err := ValidatePath(path); err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	sa, sd, err := security()
	if err != nil {
		return nil, err
	}
	disposition := uint32(windows.CREATE_NEW)
	access := uint32(windows.GENERIC_READ | windows.GENERIC_WRITE | windows.READ_CONTROL | windows.WRITE_DAC)
	if flags&os.O_WRONLY != 0 {
		access = windows.GENERIC_WRITE | windows.READ_CONTROL | windows.WRITE_DAC
	}
	h, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, sa, disposition, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_WRITE_THROUGH, 0)
	created := err == nil
	if errors.Is(err, windows.ERROR_FILE_EXISTS) && flags&os.O_EXCL == 0 {
		h, err = windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_WRITE_THROUGH, 0)
	}

	if err != nil {
		return nil, err
	}
	if created {
		if err := protect(h, sd); err != nil {
			windows.CloseHandle(h)
			return nil, err
		}
	}

	if err := Check(h, false); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// Directory creates or pins the private state directory without sharing delete
// access, so it cannot be substituted while the identity file is in use. Parents
// must be trusted, as with the Unix path-based open. It never repairs unsafe ACLs.
func Directory(path string) (*os.File, error) {
	if err := ValidatePath(path); err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	sa, sd, err := security()
	if err != nil {
		return nil, err
	}
	err = windows.CreateDirectory(name, sa)
	created := err == nil
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	if created {
		if err := protect(h, sd); err != nil {
			windows.CloseHandle(h)
			return nil, err
		}
	}

	if err := Check(h, true); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// Explicitly protect newly created objects against future inheritance. Existing
// objects are checked without altering their ACL or repairing unsafe access.
func protect(h windows.Handle, sd *windows.SECURITY_DESCRIPTOR) error {
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
