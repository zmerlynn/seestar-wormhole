//go:build windows

package privatefile

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Inspect the native descriptor independently of Check and its constants.
func assertPrivateDescriptor(t *testing.T, sd *windows.SECURITY_DESCRIPTOR) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !windows.EqualSid(owner, user.User.Sid) {
		t.Fatalf("owner = %v, %v; want current process user", owner, err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("control = %x, %v; want protected DACL", control, err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatalf("DACL = %v, %v", acl, err)
	}
	want := map[string]bool{user.User.Sid.String(): true, "S-1-5-18": true}
	if int(acl.AceCount) != len(want) {
		t.Fatalf("ACE count = %d; want %d (%v)", acl.AceCount, len(want), want)
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || ace.Mask != 0x001f01ff {
			t.Fatalf("ACE %d: type %d flags %x mask %x; want allow, no flags, FILE_ALL_ACCESS 001f01ff", i, ace.Header.AceType, ace.Header.AceFlags, ace.Mask)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if !want[sid] {
			t.Fatalf("unexpected or duplicate ACE SID %s", sid)
		}
		delete(want, sid)
	}
	if len(want) != 0 {
		t.Fatalf("missing ACE SIDs %v", want)
	}
}

func assertPrivateHandle(t *testing.T, h windows.Handle) {
	t.Helper()
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateDescriptor(t, sd)
}

func TestWindowsCreationDescriptor(t *testing.T) {
	// This exercises the OS SDDL codec even on Wine filesystems that strip
	// protection when installing the descriptor on a file.
	_, sd, err := security()
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateDescriptor(t, sd)
}

func TestWindowsPrivateDirectoryDescriptor(t *testing.T) {
	d, err := Directory(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	assertPrivateHandle(t, windows.Handle(d.Fd()))
}

func TestWindowsUnsafeDACLs(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	base := "(A;;FA;;;" + user.User.Sid.String() + ")"
	if !user.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		base += "(A;;FA;;;SY)"
	}
	for _, tc := range []struct {
		name, dacl string
		protect    bool
	}{
		{"administrators", "D:P" + base + "(A;;FA;;;BA)", true},
		{"inheritance-enabled", "D:" + base, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, err := Directory(filepath.Join(t.TempDir(), "parent"))
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			path := filepath.Join(parent.Name(), "file")
			f, err := Open(path, os.O_RDWR|os.O_CREATE|os.O_EXCL)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			h := windows.Handle(f.Fd())
			assertPrivateHandle(t, h)
			sd, err := windows.SecurityDescriptorFromString(tc.dacl)
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
			if tc.protect {
				flags = windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION
			}
			if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, flags, nil, nil, acl, nil); err != nil {
				t.Fatal(err)
			}
			// Verify the installed attack fixture, rather than accepting an API success.
			actual, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			control, _, err := actual.Control()
			if err != nil {
				t.Fatal(err)
			}
			if (control&windows.SE_DACL_PROTECTED != 0) != tc.protect {
				t.Fatalf("fixture protection = %x; want %t", control, tc.protect)
			}
			actualACL, _, err := actual.DACL()
			if err != nil || actualACL == nil || actualACL.AceCount != acl.AceCount {
				t.Fatalf("fixture ACE count differs: %v", err)
			}
			// Both fixture ACLs must retain precisely the requested native ACEs.
			for i := uint32(0); i < uint32(acl.AceCount); i++ {
				var want, got *windows.ACCESS_ALLOWED_ACE
				if err := windows.GetAce(acl, i, &want); err != nil {
					t.Fatal(err)
				}
				if err := windows.GetAce(actualACL, i, &got); err != nil {
					t.Fatal(err)
				}
				if got.Header.AceType != want.Header.AceType || got.Header.AceFlags != want.Header.AceFlags || got.Mask != want.Mask || !windows.EqualSid((*windows.SID)(unsafe.Pointer(&got.SidStart)), (*windows.SID)(unsafe.Pointer(&want.SidStart))) {
					t.Fatalf("native attack fixture ACE %d differs from requested ACE", i)
				}
			}
			if err := Check(h, false); err == nil {
				t.Fatal("unsafe DACL accepted")
			}
		})
	}
}
