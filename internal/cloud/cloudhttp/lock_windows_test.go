//go:build windows

package cloudhttp

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/go-json-experiment/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"golang.org/x/sys/windows"
)

type identityResult struct{ ID, Phase, Reason string }

// A real child process owns the handle. Pipes provide barriers; no sleeps or
// timing guesses establish acquisition, release or repair ordering.
func TestWindowsIdentityProcess(t *testing.T) {
	role := os.Getenv("WORMHOLE_IDENTITY_TEST_ROLE")
	if role == "" {
		return
	}
	f, err := os.OpenFile(os.Getenv("WORMHOLE_IDENTITY_TEST_PATH"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if role == "repair" || role == "queued-repair" {
		if role == "queued-repair" {
			fmt.Println("ready")
			input := bufio.NewScanner(os.Stdin)
			if !input.Scan() || input.Text() != "repair" {
				t.Fatal("missing repair barrier")
			}
		}
		fmt.Println("started")
		id, err := persistentIdentity(f)
		result := identityResult{ID: id}
		if err != nil {
			result.Phase, result.Reason = diagnostic.Fields(err, "")
		}
		if err := json.MarshalEncode(jsontext.NewEncoder(os.Stdout), result); err != nil {
			t.Fatal(err)
		}
		return
	}
	var lock windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &lock); err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	input := bufio.NewScanner(os.Stdin)
	if !input.Scan() || input.Text() != "close" {
		t.Fatal("missing close barrier")
	}
	// Deliberately do not UnlockFileEx: handle closure itself must release it.
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("closed")
	if !input.Scan() || input.Text() != "exit" {
		t.Fatal("missing exit barrier")
	}
}

type identityChild struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Reader
	stderr bytes.Buffer
}

func startIdentityChild(t *testing.T, path, role, barrier string) *identityChild {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	child := &identityChild{cmd: exec.CommandContext(ctx, exe, "-test.run=^TestWindowsIdentityProcess$")}
	child.cmd.Env = append(os.Environ(), "WORMHOLE_IDENTITY_TEST_ROLE="+role, "WORMHOLE_IDENTITY_TEST_PATH="+path)
	child.cmd.Stderr = &child.stderr
	child.input, err = child.cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	output, err := child.cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	child.output = bufio.NewReader(output)
	if err := child.cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { child.input.Close(); cancel(); child.cmd.Wait() })
	child.barrier(t, barrier)
	return child
}

func (c *identityChild) barrier(t *testing.T, want string) {
	t.Helper()
	got, err := c.output.ReadString('\n')
	if err != nil || got != want+"\n" {
		t.Fatalf("child barrier %q, %v; want %q", got, err, want)
	}
}

func (c *identityChild) result(t *testing.T) identityResult {
	t.Helper()
	var result identityResult
	line, err := c.output.ReadBytes('\n')
	if err != nil {
		t.Fatalf("child result: %v", err)
	}
	if err := json.Unmarshal(line, &result, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatalf("child result %q: %v", line, err)
	}
	if err := c.cmd.Wait(); err != nil {
		t.Fatalf("child exit: %v (%s)", err, &c.stderr)
	}
	return result
}

func assertLockTimeout(t *testing.T, result identityResult) {
	t.Helper()
	if result.ID != "" || result.Phase != "state_identity" || result.Reason != "lock_timeout" {
		t.Fatalf("contended repair = %+v; want state_identity/lock_timeout with no identity", result)
	}
}

func TestWindowsIdentityLockRelease(t *testing.T) {
	for _, release := range []string{"close", "crash"} {
		t.Run(release, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "identity")
			const want = "12345678-1234-4234-8234-123456789abc"
			if err := os.WriteFile(path, []byte(want), 0600); err != nil {
				t.Fatal(err)
			}
			holder := startIdentityChild(t, path, "holder", "locked")
			contender := startIdentityChild(t, path, "repair", "started")
			assertLockTimeout(t, contender.result(t))
			if release == "crash" {
				if err := holder.cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := holder.cmd.Wait(); err == nil {
					t.Fatal("killed child exited successfully")
				}
			} else {
				if _, err := io.WriteString(holder.input, "close\n"); err != nil {
					t.Fatal(err)
				}
				holder.barrier(t, "closed")
			}
			// In the close case the holder process is still alive at this point.
			next := startIdentityChild(t, path, "repair", "started").result(t)
			if next.ID != want || next.Phase != "" || next.Reason != "" {
				t.Fatalf("reacquisition = %+v; want %s", next, want)
			}
			if release == "close" {
				if _, err := io.WriteString(holder.input, "exit\n"); err != nil {
					t.Fatal(err)
				}
				if err := holder.cmd.Wait(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestWindowsEmptyIdentityRepairContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	holder := startIdentityChild(t, path, "holder", "locked")
	blocked := startIdentityChild(t, path, "repair", "started")
	assertLockTimeout(t, blocked.result(t))
	info, err := os.Stat(path)
	if err != nil || info.Size() != 0 {
		t.Fatalf("locked empty file changed: %v, %v", info, err)
	}
	if _, err := io.WriteString(holder.input, "close\n"); err != nil {
		t.Fatal(err)
	}
	holder.barrier(t, "closed")
	// Queue concurrent repairs behind an independently acquired native lock.
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lock windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &lock); err != nil {
		t.Fatal(err)
	}
	children := make([]*identityChild, 6)
	for i := range children {
		children[i] = startIdentityChild(t, path, "queued-repair", "ready")
	}
	for _, child := range children {
		if _, err := io.WriteString(child.input, "repair\n"); err != nil {
			t.Fatal(err)
		}
		child.barrier(t, "started")
	}
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &lock); err != nil {
		t.Fatal(err)
	}
	first := ""
	for _, child := range children {
		result := child.result(t)
		if result.Phase != "" || result.Reason != "" || len(result.ID) != 36 {
			t.Fatalf("repair = %+v; want UUID", result)
		}
		if first == "" {
			first = result.ID
		}
		if result.ID != first {
			t.Fatalf("concurrent repair %s differs from %s", result.ID, first)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != first {
		t.Fatalf("repaired bytes = %q, %v; want %s", got, err, first)
	}
	if _, err := io.WriteString(holder.input, "exit\n"); err != nil {
		t.Fatal(err)
	}
	if err := holder.cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
