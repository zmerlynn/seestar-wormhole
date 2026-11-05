package privatefile

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// openOrdinary opens an ordinary folder, which passes inheritable ACEs down
// as a user's folders do, unlike the protected one [Directory] makes.
func openOrdinary(t *testing.T) *os.File {
	t.Helper()
	d, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// descriptor reads the native owner and DACL of the file at path.
func descriptor(t *testing.T, path string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd
}

// An Inherited write installs no protected DACL: the file keeps the ACEs its
// folder passes down, as a state file did before WriteFile, so a filesystem
// without ACLs can hold it. It also replaces an existing empty file.
func TestWindowsWriteFileInherited(t *testing.T) {
	d := openOrdinary(t)
	path := filepath.Join(d.Name(), "state.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(d, "state.json", []byte("header"), Inherited); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "header" {
		t.Fatalf("file = %q (%v), want the header", b, err)
	}
	sd := descriptor(t, path)
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED != 0 {
		t.Errorf("control = %x, want a DACL that inherits", control)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatalf("DACL = %v, %v", acl, err)
	}
	inherited := false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		inherited = inherited || ace.Header.AceFlags&windows.INHERITED_ACE != 0
	}
	if !inherited {
		t.Error("no ACE inherited from the folder")
	}
}
