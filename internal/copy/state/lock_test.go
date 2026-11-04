package state

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockFileExcludesSecondOpenInProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	first, err := lockFile(path, false)
	if err != nil {
		t.Fatalf("first lockFile: %v", err)
	}
	if f, err := lockFile(path, false); !errors.Is(err, ErrLocked) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("second lockFile while held: err=%v, want ErrLocked", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	again, err := lockFile(path, false)
	if err != nil {
		t.Fatalf("lockFile after close: %v, want success", err)
	}
	again.Close()
}

func TestLockFileWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	first, err := lockFile(path, false)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		f, err := lockFile(path, true)
		if err == nil {
			f.Close()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("waiting lockFile returned %v while the lock was held", err)
	case <-time.After(50 * time.Millisecond):
	}
	first.Close()
	if err := <-got; err != nil {
		t.Errorf("waiting lockFile after the release: %v", err)
	}
}

// lockHelperEnv makes the test binary act as a lock holder: it locks the
// file named by the variable, reports "locked" on stdout, and exits without
// unlocking when stdin closes.
const lockHelperEnv = "SEESTAR_COPY_STATE_LOCK_HELPER"

func TestLockHelper(t *testing.T) {
	path := os.Getenv(lockHelperEnv)
	if path == "" {
		t.Skip("run only as a subprocess")
	}
	if _, err := lockFile(path, false); err != nil {
		os.Stdout.WriteString("error: " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("locked\n")
	io.Copy(io.Discard, os.Stdin)
	os.Exit(0) // without closing the lock file
}

func TestLockFileHeldByOtherProcessUntilExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelper$")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if line != "locked\n" {
		stdin.Close()
		cmd.Wait()
		t.Fatalf("helper said %q (%v), want \"locked\\n\"", line, err)
	}
	if f, err := lockFile(path, false); !errors.Is(err, ErrLocked) {
		if f != nil {
			f.Close()
		}
		t.Errorf("lockFile while another process holds it: err=%v, want ErrLocked", err)
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	f, err := lockFile(path, false)
	if err != nil {
		t.Fatalf("lockFile after the holder exited: %v, want success", err)
	}
	f.Close()
}

// TestOpenKeepsLockFile checks that the lock file is never removed or
// replaced: removing it on unlock would let a waiting opener lock the old
// inode while a new one locks a new file at the same name.
func TestOpenKeepsLockFile(t *testing.T) {
	stateDir, dest := dirs(t)
	p, _ := Path(stateDir, dest)
	lockPath := strings.TrimSuffix(p, ".jsonl") + ".lock"
	var first os.FileInfo
	same := func(when string) {
		t.Helper()
		fi, err := os.Stat(lockPath)
		switch {
		case err != nil:
			t.Fatalf("%s: %v", when, err)
		case first == nil:
			first = fi
		case !os.SameFile(first, fi):
			t.Errorf("%s: the lock file was replaced", when)
		}
	}

	s := mustOpen(t, stateDir, dest, nil)
	same("while open")
	fail := true
	hookSync(t, func(*os.File) error {
		if fail {
			fail = false
			return errors.New("disk on fire")
		}
		return nil
	})
	if err := s.Append(m31Copied(t)); err == nil {
		t.Fatal("Append succeeded, want the injected fsync error")
	}
	same("after a failed append")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	same("after Close")

	s = mustOpen(t, stateDir, dest, nil)
	same("after reopening")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	same("after the second Close")

	if err := os.WriteFile(p, []byte(headerLine("/elsewhere")), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(stateDir, dest, nil); err == nil {
		s.Close()
		t.Fatal("Open of a state for another destination succeeded")
	}
	same("after a refusal")
}
