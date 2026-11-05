//go:build unix

package privatefile

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// readWritten reads the file WriteFile wrote at path under p through
// [OpenRead], which accepts it as private under either policy on Unix.
func readWritten(path string, _ Policy) ([]byte, error) {
	f, err := OpenRead(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// umaskChildEnv makes the test binary run the body of
// TestWriteFileRestrictiveUmask. The umask belongs to the whole process, so
// the parent sets it only in a child.
const umaskChildEnv = "SEESTAR_WORMHOLE_PRIVATEFILE_UMASK_CHILD"

// A umask that takes the owner's write bit away still leaves WriteFile's file
// exactly 0600, under both policies.
func TestWriteFileRestrictiveUmask(t *testing.T) {
	if os.Getenv(umaskChildEnv) == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestWriteFileRestrictiveUmask$", "-test.v")
		cmd.Env = append(os.Environ(), umaskChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestWriteFileRestrictiveUmask") {
			t.Fatalf("child: %v\n%s", err, out)
		}
		return
	}
	d := privateDir(t)
	syscall.Umask(0o277)
	for _, p := range []Policy{Private, Inherited} {
		if err := WriteFile(d, "state.json", []byte("content"), p); err != nil {
			t.Fatalf("WriteFile(%v) under umask 0277: %v", p, err)
		}
		if fi, err := os.Lstat(filepath.Join(d.Name(), "state.json")); err != nil || fi.Mode() != 0600 {
			t.Errorf("mode = %v (%v), want -rw-------", fi.Mode(), err)
		}
	}
}
