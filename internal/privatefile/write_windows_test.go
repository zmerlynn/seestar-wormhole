package privatefile

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// readWritten reads the file WriteFile wrote at path under p. A Private file
// is read through [OpenRead]; an Inherited one keeps the unprotected DACL its
// folder passes down, which OpenRead refuses, so it is read as any file is.
func readWritten(path string, p Policy) ([]byte, error) {
	if p == Inherited {
		return os.ReadFile(path)
	}
	f, err := OpenRead(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

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

// A Private write replacing an earlier Private file leaves the native private
// descriptor, which Replace carries over from the file it replaces.
func TestWindowsWriteFilePrivateReplace(t *testing.T) {
	d := privateDir(t)
	path := filepath.Join(d.Name(), "config.json")
	for _, content := range []string{"first", "second"} {
		if err := WriteFile(d, "config.json", []byte(content), Private); err != nil {
			t.Fatal(err)
		}
	}
	assertPrivateDescriptor(t, descriptor(t, path))
}

// A Private write refuses to replace an empty file with an inherited DACL,
// which Replace would keep, and leaves it as it was.
func TestWindowsWriteFilePrivateRefusesInheritedDACL(t *testing.T) {
	d := openOrdinary(t)
	path := filepath.Join(d.Name(), "config.json")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(d, "config.json", []byte("new"), Private); err == nil {
		t.Fatal("WriteFile replaced a file with an inherited DACL")
	}
	if b, err := os.ReadFile(path); err != nil || len(b) != 0 {
		t.Errorf("file = %q (%v), want it empty", b, err)
	}
	control, _, err := descriptor(t, path).Control()
	if err != nil || control&windows.SE_DACL_PROTECTED != 0 {
		t.Errorf("control = %x (%v), want the inherited DACL kept", control, err)
	}
}
