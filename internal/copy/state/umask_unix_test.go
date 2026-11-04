//go:build darwin || linux

package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// umaskChildEnv makes the test binary run the body of
// TestOpenHeaderModeUnderRestrictiveUmask, in a directory it names, with a
// umask that strips every permission bit. The umask is process-wide, so it is
// only ever set in that child.
const umaskChildEnv = "SEESTAR_COPY_STATE_UMASK_CHILD"

// TestOpenHeaderModeUnderRestrictiveUmask checks that a new header is 0600
// whatever the umask: with the directories and lock files already there, a
// umask of 0777 does not stop Open, and the header must still be readable by
// its owner.
func TestOpenHeaderModeUnderRestrictiveUmask(t *testing.T) {
	if root := os.Getenv(umaskChildEnv); root != "" {
		openUnderUmask(t, root)
		return
	}
	root := t.TempDir()
	stateDir, dest := filepath.Join(root, "state"), filepath.Join(root, "dest")
	mkdir(t, dest)
	mkdir(t, filepath.Join(stateDir, "sync"))
	p, _ := Path(stateDir, dest)
	for _, lock := range []string{strings.TrimSuffix(p, ".jsonl") + ".lock", filepath.Join(stateDir, "sync", registryName)} {
		if err := os.WriteFile(lock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestOpenHeaderModeUnderRestrictiveUmask$", "-test.v")
	cmd.Env = append(os.Environ(), umaskChildEnv+"="+root)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestOpenHeaderModeUnderRestrictiveUmask") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("header mode = %v, want 0600", got)
	}
	if got := readFile(t, p); got != headerLine(dest) {
		t.Errorf("state file = %q, want %q", got, headerLine(dest))
	}
}

func openUnderUmask(t *testing.T, root string) {
	stateDir, dest := filepath.Join(root, "state"), filepath.Join(root, "dest")
	syscall.Umask(0o777)
	s, err := Open(stateDir, dest, nil)
	if err != nil {
		t.Fatalf("Open with umask 0777: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	p, _ := Path(stateDir, dest)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("header mode = %v, want 0600", got)
	}
}
