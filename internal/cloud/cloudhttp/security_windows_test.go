//go:build windows

package cloudhttp

import (
	"golang.org/x/sys/windows"
	"testing"
	"unsafe"
)

// Inspect the native descriptor independently of Check and its constants.
func assertIdentityDescriptor(t *testing.T, sd *windows.SECURITY_DESCRIPTOR) {
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

func assertIdentityHandle(t *testing.T, h windows.Handle) {
	t.Helper()
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	assertIdentityDescriptor(t, sd)
}
