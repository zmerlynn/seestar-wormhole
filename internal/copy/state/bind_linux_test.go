package state

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// bindChildEnv makes the test binary run the body of
// TestOpenRefusesStateUnderBindOfDest in the directory it names. The parent
// starts that child in new user and mount namespaces, mapping the caller to
// root of the new user namespace so that the child keeps the capability to
// mount after exec. Its bind mounts therefore need no privilege outside the
// namespace, and are gone when it exits.
const bindChildEnv = "SEESTAR_COPY_STATE_BIND_CHILD"

// TestOpenRefusesStateUnderBindOfDest puts the state directory under a bind
// mount of the destination. No symbolic link is involved, so only comparing
// the folders themselves can see that it is inside the destination.
func TestOpenRefusesStateUnderBindOfDest(t *testing.T) {
	if root := os.Getenv(bindChildEnv); root != "" {
		openUnderBind(t, root)
		return
	}
	root := t.TempDir()
	dest := filepath.Join(root, "dest")
	mkdir(t, filepath.Join(dest, "inner"))
	mkdir(t, filepath.Join(root, "alias"))
	syncDest := filepath.Join(root, "sync-dest")
	mkdir(t, syncDest)
	mkdir(t, filepath.Join(root, "state", "sync"))
	cmd := exec.Command(os.Args[0], "-test.run=^TestOpenRefusesStateUnderBindOfDest$", "-test.v")
	cmd.Env = append(os.Environ(), bindChildEnv+"="+root)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err != nil && !errors.As(err, &exit):
		t.Skipf("cannot start a process in new user and mount namespaces: %v", err)
	case strings.Contains(string(out), "--- SKIP: TestOpenRefusesStateUnderBindOfDest"):
		t.Skipf("child skipped:\n%s", out)
	case err != nil || !strings.Contains(string(out), "--- PASS: TestOpenRefusesStateUnderBindOfDest"):
		t.Fatalf("child: %v\n%s", err, out)
	}
	emptyOrMissing(t, filepath.Join(dest, "state"))
	emptyOrMissing(t, filepath.Join(dest, "inner"))
	emptyOrMissing(t, syncDest)
	emptyOrMissing(t, filepath.Join(root, "state", "sync"))
}

func openUnderBind(t *testing.T, root string) {
	dest, alias := filepath.Join(root, "dest"), filepath.Join(root, "alias")
	if err := syscall.Mount(dest, alias, "", syscall.MS_BIND, ""); err != nil {
		t.Skipf("cannot bind mount: %v", err)
	}
	// The second state directory's deepest existing ancestor is a folder in
	// the destination, not the destination itself.
	for _, stateDir := range []string{filepath.Join(alias, "state"), filepath.Join(alias, "inner", "state")} {
		s, err := Open(stateDir, dest, nil)
		if err == nil {
			s.Close()
			t.Fatalf("Open put the state at %s, under a bind mount of the destination", stateDir)
		}
		want := "state directory " + stateDir + " is inside the destination " + dest + ": its ancestor " + alias + " is the same folder as the destination"
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Open error = %q, want it to contain %q", err, want)
		}
	}
	emptyOrMissing(t, filepath.Join(dest, "state"))
	emptyOrMissing(t, filepath.Join(dest, "inner"))

	// An existing sync/ of a state directory outside the destination is
	// itself a bind mount of the whole destination, so the only folder that
	// matches is the candidate itself, not any of its ancestors.
	syncDest, stateDir := filepath.Join(root, "sync-dest"), filepath.Join(root, "state")
	syncDir := filepath.Join(stateDir, "sync")
	if err := syscall.Mount(syncDest, syncDir, "", syscall.MS_BIND, ""); err != nil {
		t.Skipf("cannot bind mount: %v", err)
	}
	s, err := Open(stateDir, syncDest, nil)
	if err == nil {
		s.Close()
		t.Fatalf("Open put the state in %s, a bind mount of the destination", syncDir)
	}
	want := "state directory " + syncDir + " is inside the destination " + syncDest + ": its ancestor " + syncDir + " is the same folder as the destination"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("Open error = %q, want it to contain %q", err, want)
	}
	emptyOrMissing(t, syncDest)
}
