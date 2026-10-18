//go:build windows

package privatefile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestADSRefusalPreservesBase(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, access := range []string{"public", "private"} {
		t.Run(access, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "base")
			want := []byte("existing base bytes must survive")
			if err := os.WriteFile(path, want, 0600); err != nil {
				t.Fatal(err)
			}
			name, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(h)
			dacl := "D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)"
			if access == "public" {
				dacl += "(A;;FA;;;WD)"
			}
			sd, err := windows.SecurityDescriptorFromString(dacl)
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
			snapshot := func() string {
				t.Helper()
				sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
				if err != nil {
					t.Fatal(err)
				}
				return sd.String()
			}
			before := snapshot()
			for _, suffix := range []string{":stream", "::$DATA"} {
				for _, flags := range []int{os.O_WRONLY | os.O_CREATE | os.O_EXCL, os.O_RDWR | os.O_CREATE} {
					f, err := Open(path+suffix, flags)
					if f != nil {
						f.Close()
					}
					if err == nil || !strings.Contains(err.Error(), "alternate data streams unsupported") {
						t.Fatalf("Open(%q, %x) = %v, want pre-create stream refusal", suffix, flags, err)
					}
					if after := snapshot(); after != before {
						t.Fatalf("descriptor changed: before %s after %s", before, after)
					}
					got, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("base bytes %q, %v; want %q", got, err, want)
					}
				}
			}
		})
	}
}

func TestWindowsPathSyntax(t *testing.T) {
	for _, path := range []string{`secret`, `C:\private\secret`, `C:secret`, `\\server\share\secret`, `\\?\C:\private\secret`} {
		if err := ValidatePath(path); err != nil {
			t.Errorf("safe path %q: %v", path, err)
		}
	}
	for _, path := range []string{`secret:stream`, `C:\secret:stream`, `C:\secret::$DATA`, `C:secret:stream`, `\\server\share\secret:stream`, `\\server:stream\share\secret`, `\\?\C:\secret:stream`} {
		if err := ValidatePath(path); err == nil {
			t.Errorf("stream path accepted: %q", path)
		}
		if f, err := Directory(path); err == nil {
			f.Close()
			t.Errorf("stream directory accepted: %q", path)
		}
	}
}
