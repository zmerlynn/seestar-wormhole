//go:build gui

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestConsoleControlCancellation(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(cwd, "main.go")
	source, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	// Build an actual windowsgui command, retaining main, package init,
	// parsing and bootstrap. Replace only the runner with an offline receipt.
	const dispatch = "windows, runParsed)"
	if strings.Count(string(source), dispatch) != 1 {
		t.Fatal("command runner dispatch changed; update the test overlay")
	}
	mainOverlay := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainOverlay, []byte(strings.Replace(string(source), dispatch, "windows, consoleRegressionRunner)", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	overlay, err := json.Marshal(struct{ Replace map[string]string }{map[string]string{
		mainPath: mainOverlay,
		filepath.Join(cwd, "console_regression_fixture_windows.go"): filepath.Join(cwd, "testdata", "console_runner_windows.go"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "console-gui.exe")
	build := exec.CommandContext(t.Context(), "go", "build", "-tags=gui,production", "-overlay="+overlayPath, "-ldflags=-H=windowsgui", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build windowsgui command: %v\n%s", err, output)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	// A separate console avoids relying on the CI agent having one, and keeps
	// control events away from the test runner and other jobs. Hide the console.
	helper := exec.CommandContext(ctx, testBinary, "-test.run=^TestConsoleControlProcess$", "-test.v")
	helper.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE, HideWindow: true}
	helper.Env = append(os.Environ(), "WORMHOLE_CONSOLE_TEST_EXE="+binary, "WORMHOLE_CONSOLE_TEST_DIR="+dir)
	if output, err := helper.CombinedOutput(); err != nil {
		t.Fatalf("isolated console helper: %v\n%s", err, output)
	}
}

func TestConsoleControlProcess(t *testing.T) {
	binary := os.Getenv("WORMHOLE_CONSOLE_TEST_EXE")
	if binary == "" {
		return
	}
	dir := os.Getenv("WORMHOLE_CONSOLE_TEST_DIR")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "proxy")
	child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	defer func() {
		cancel()
		// CommandContext kills a stalled child; always join before TempDir removal.
		if done != nil {
			<-done
		}
	}()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		select {
		case err := <-done:
			done = nil
			t.Fatalf("GUI command exited before ready: %v", err)
		case <-ctx.Done():
			t.Fatal("GUI command did not become ready")
		case <-tick.C:
		}
	}
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(child.Process.Pid)); err != nil {
		t.Fatalf("GenerateConsoleCtrlEvent: %v", err)
	}
	err := <-done
	done = nil
	if err != nil {
		t.Fatalf("GUI command did not exit through cleanup: %v", err)
	}
	receipt, err := os.ReadFile(filepath.Join(dir, "released"))
	if err != nil || string(receipt) != "cleanup complete\n" {
		t.Fatalf("release receipt = %q, error = %v", receipt, err)
	}
}

func TestConsoleTerminatingEventsJoinCleanup(t *testing.T) {
	for _, event := range []uint32{windows.CTRL_CLOSE_EVENT, windows.CTRL_LOGOFF_EVENT, windows.CTRL_SHUTDOWN_EVENT} {
		t.Run(string(rune('0'+event)), func(t *testing.T) {
			previous := attachedConsole
			c, err := newConsoleControl()
			if err != nil {
				t.Fatal(err)
			}
			attachedConsole = c
			defer func() { attachedConsole = previous }()
			ctx, stop := notifyContext(t.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			done := make(chan uintptr, 1)
			go func() { done <- c.handle(event) }()
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("console event did not cancel the command")
			}
			select {
			case <-done:
				t.Fatal("handler returned before command cleanup")
			default:
			}
			stop() // Bootstrap calls stop only after runner and log cleanup return.
			select {
			case handled := <-done:
				if handled != 1 {
					t.Fatalf("handled = %d", handled)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not return after cleanup")
			}
		})
	}
}

func TestConsoleEarlyControlEvent(t *testing.T) {
	previous := attachedConsole
	c, err := newConsoleControl()
	if err != nil {
		t.Fatal(err)
	}
	attachedConsole = c
	defer func() { attachedConsole = previous }()
	if c.handle(99) != 0 {
		t.Fatal("unknown console event was consumed")
	}
	if c.handle(windows.CTRL_BREAK_EVENT) != 1 || c.handle(windows.CTRL_C_EVENT) != 1 {
		t.Fatal("interrupt was not handled")
	}
	ctx, stop := notifyContext(t.Context(), os.Interrupt)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("event received before bootstrap was lost")
	}
}
