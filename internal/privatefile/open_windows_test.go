//go:build windows

package privatefile

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrivateCreationAndRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret")
	f, err := Open(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	assertPrivateHandle(t, windows.Handle(f.Fd()))
	if other, err := Open(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL); err == nil {
		other.Close()
		t.Fatal("existing file accepted")
	}
	// An inherited/public DACL must never stand in for Unix 0600.
	public := filepath.Join(t.TempDir(), "public")
	if err := os.WriteFile(public, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := os.Open(public)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := Check(windows.Handle(p.Fd()), false); err == nil {
		t.Fatal("inherited DACL accepted")
	}
}

func TestReparseRefusal(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	f, err := Open(target, os.O_RDWR|os.O_CREATE|os.O_EXCL)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	if f, err := Open(link, os.O_RDWR|os.O_CREATE); err == nil {
		f.Close()
		t.Fatal("reparse point followed")
	} else if err.Error() != "private file: unsafe type" {
		t.Fatalf("reparse refusal = %v; want unsafe type from the opened reparse handle", err)
	}
}

func TestInheritedDACLRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "public")
	if err := os.WriteFile(path, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	// Use the OS codec to install a known unsafe Everyone allow ACE. This test
	// can exercise refusal even when Wine cannot preserve a private DACL.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if err := Check(h, false); err == nil {
		t.Fatal("Everyone DACL accepted")
	}
}

func TestReparseRefusalBeforeACL(t *testing.T) {
	// The reparse boundary must precede ACL inspection, even for a public target.
	// This reaches native CreateFile without requiring private-file creation.
	root := t.TempDir()
	target := filepath.Join(root, "public-target")
	if err := os.WriteFile(target, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(h, &info)
	windows.CloseHandle(h)
	if err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		t.Fatalf("fixture is not a native reparse handle: attributes %x, %v", info.FileAttributes, err)
	}
	f, err := Open(link, os.O_RDWR|os.O_CREATE)
	if f != nil {
		f.Close()
	}
	if err == nil || err.Error() != "private file: unsafe type" {
		t.Fatalf("reparse refusal = %v; want unsafe type before any target ACL inspection", err)
	}
}
