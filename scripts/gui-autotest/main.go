//go:build guitest

// A small native-process harness. The fixture drives the real DOM itself;
// this process supplies OS events and checks exit, listener, and lock lifetime.
package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/guitest"
)

func main() {
	binary := flag.String("binary", "", "gui,guitest binary (or executable inside a Mac bundle)")
	out := flag.String("out", "", "artifact directory")
	scenarios := flag.String("scenarios", "fresh,quit-connecting,quit-ready,close-connecting,close-ready,activate,strand,errors,term,interrupt,"+guitest.UXScenarios, "comma-separated scenarios")
	verify := flag.String("verify", "", "fixture,production,plain-gui binaries: inspect only")
	timeout := flag.Duration("timeout", 100*time.Second, "bound per scenario including relaunch")
	flag.Parse()
	if *verify != "" {
		if err := verifyIsolation(strings.Split(*verify, ",")); err != nil {
			fmt.Println("SCENARIO build-isolation FAIL:", err)
			os.Exit(1)
		}
		return
	}
	if *binary == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	bin, err := filepath.Abs(*binary)
	if err != nil {
		panic(err)
	}
	dir, err := filepath.Abs(*out)
	if err != nil {
		panic(err)
	}
	failed := false
	for _, s := range strings.Split(*scenarios, ",") {
		if runtime.GOOS == "windows" && (s == "term" || s == "interrupt") {
			fmt.Printf("SCENARIO %s SKIP: GUI subsystem has no attached console; CTRL_BREAK/console-close not exercised\n", s)
			continue
		}
		if s == "apple" && runtime.GOOS != "darwin" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		err := runScenario(ctx, bin, filepath.Join(dir, s), s)
		cancel()
		if err != nil {
			printFailure(os.Stdout, s, filepath.Join(dir, s), err)
			failed = true
		} else {
			if s == "relaunch" {
				fmt.Printf("SCENARIO %s PASS: setup DOM, screenshot, Quit exit 0, listener closed, fresh lock acquisition (no runner)\n", s)
			} else {
				fmt.Printf("SCENARIO %s PASS: DOM, screenshots, release join, exit 0, listener closed, fresh lock acquisition\n", s)
			}
		}
	}
	if failed {
		os.Exit(1)
	}
}

// Keep startup failures visible even if an artifact upload fails. Strip the
// entire suffix after '#' on every line, including errors and nested launches.
func printFailure(out io.Writer, scenario, dir string, err error) {
	clean := func(line string) string { return strings.SplitN(line, "#", 2)[0] }
	fmt.Fprintf(out, "SCENARIO %s FAIL: %s\n", scenario, clean(err.Error()))
	filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || entry.Name() != "process.log" {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		lines := strings.Split(strings.TrimRight(string(b), "\r\n"), "\n")
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		name, _ := filepath.Rel(dir, path)
		fmt.Fprintf(out, "--- %s/%s (last %d lines) ---\n", scenario, name, len(lines))
		for _, line := range lines {
			fmt.Fprintln(out, clean(line))
		}
		return nil
	})
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func relaunchScenario(scenario string) string {
	if scenario == "saved-password" {
		return "saved-relaunch"
	}
	// The OS preference survives exit; keep the fixture's expectation with it.
	if strings.HasPrefix(scenario, "dark") {
		return "dark-relaunch"
	}
	return "relaunch"
}

func launch(ctx context.Context, bin, dir, root, scenario string) (*exec.Cmd, <-chan error, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, err
	}
	log, err := os.Create(filepath.Join(dir, "process.log"))
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "ui", "--state-dir", root)
	// An engine child may inherit output handles beyond the fixture lifetime.
	cmd.WaitDelay = 2 * time.Second
	// No user config, account, password provider, proxy ports, or network endpoint.
	cmd.Env = append(os.Environ(), "SEESTAR_GUITEST_OUT="+dir, "SEESTAR_GUITEST_SCENARIO="+scenario)
	// Drain through a pipe: the CLI may reopen stderr with an independent file
	// offset. Direct regular-file descriptors let its final error overwrite earlier
	// stdout diagnostics. The shared writer also keeps both streams in one pipe.
	output := struct{ io.Writer }{log}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if errors.Is(err, exec.ErrWaitDelay) {
			fmt.Fprintln(log, "Warning: inherited process output remained open after exit; capture stopped")
			err = nil // ErrWaitDelay means the process itself exited successfully.
		}
		log.Close()
		done <- err
		close(done)
	}()
	return cmd, done, nil
}

func runScenario(ctx context.Context, bin, dir, scenario string) error {
	// A rerun gets fresh state and cannot accidentally use a previous checkpoint.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	// Engine-owned files can remain locked after exit. Never upload them.
	state, err := os.MkdirTemp("", "seestar-guitest-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(state)
	root := filepath.Join(state, "state")
	cmd, done, err := launch(ctx, bin, dir, root, scenario)
	if err != nil {
		return err
	}
	defer func() { cmd.Process.Kill(); <-done }()
	waitFile := func(name string) error {
		var early error
		err := guitest.Wait(ctx, name, func() bool {
			select {
			case exitErr := <-done:
				early = fmt.Errorf("process exited before %s: %v (see process.log)", name, exitErr)
				return true
			default:
			}
			return exists(name)
		})
		return errors.Join(err, early)
	}
	// Read only the ephemeral listener address; never log its capability token.
	if err := waitFile(filepath.Join(root, "ui-runtime.json")); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(root, "ui-runtime.json"))
	if err != nil {
		return err
	}
	var record struct {
		Port int `json:"port"`
	}
	if err = json.Unmarshal(b, &record); err != nil {
		return err
	}
	if record.Port < 1 || record.Port > 65535 {
		return errors.New("invalid listener port")
	}
	if scenario == "activate" || scenario == "strand" || scenario == "term" || scenario == "interrupt" || scenario == "apple" || (runtime.GOOS == "windows" && strings.HasPrefix(scenario, "close-")) {
		if err := waitFile(filepath.Join(dir, "trigger")); err != nil {
			return err
		}
		switch scenario {
		case "activate", "strand":
			if scenario == "strand" && !exists(filepath.Join(dir, "off-origin")) {
				return errors.New("second launch before off-origin assertion")
			}
			second, secondDone, err := launch(ctx, bin, filepath.Join(dir, "second"), root, scenario)
			if err != nil {
				return err
			}
			defer func() { second.Process.Kill(); <-secondDone }()
			select {
			case err := <-secondDone:
				if err != nil {
					return fmt.Errorf("second process: %w", err)
				}
			case <-ctx.Done():
				return errors.New("second process did not exit")
			}
			// The owner must remain live and complete its real Show/Focus assertion.
			if exists(filepath.Join(dir, "joined")) {
				return errors.New("owner exited during activation")
			}
		case "term":
			err = terminate(cmd.Process, false)
		case "interrupt":
			err = terminate(cmd.Process, true)
		case "apple":
			receipt, quitErr := exec.CommandContext(ctx, "osascript", "-e", `tell application id "net.zml.seestar-wormhole" to quit`).CombinedOutput()
			if e := os.WriteFile(filepath.Join(dir, "apple-event.log"), receipt, 0600); e != nil {
				return e
			}
			if quitErr != nil || strings.Contains(string(receipt), "-128") {
				return fmt.Errorf("Apple quit must be accepted, got %v: %s", quitErr, receipt)
			}
		default:
			// CloseMainWindow sends WM_CLOSE, the same path as the spike workflow.
			script := `$p=Get-Process -Id ` + strconv.Itoa(cmd.Process.Pid) + `; if (-not $p.CloseMainWindow()) { throw 'WM_CLOSE failed' }`
			b, e := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
			if e != nil {
				return fmt.Errorf("WM_CLOSE: %w: %s", e, b)
			}
			fmt.Println("native close path: Windows WM_CLOSE via CloseMainWindow")
		}
		if err != nil {
			return err
		}
	}
	if strings.HasPrefix(scenario, "close-") && runtime.GOOS != "windows" {
		fmt.Println("native close path:", runtime.GOOS, "Wails window.Close() -> WindowClosing hook")
	}
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("process: %w (see process.log)", err)
		}
	case <-ctx.Done():
		return errors.New("process did not exit within bound")
	}
	if scenario == "strand" && !exists(filepath.Join(dir, "restored")) {
		return errors.New("missing authenticated window recovery assertion")
	}
	if !exists(filepath.Join(dir, "joined")) {
		return errors.New("missing joined-window receipt (see process.log)")
	}
	if guitest.RequiresRelease(scenario) && !exists(filepath.Join(dir, "release-wait-observed")) {
		return errors.New("missing slow-release window assertion")
	}
	conn, e := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(record.Port)), 300*time.Millisecond)
	if e == nil {
		conn.Close()
		return errors.New("listener survived exit")
	}
	if exists(filepath.Join(root, "ui-runtime.json")) {
		return errors.New("runtime record survived exit")
	}
	child, childDone, e := launch(ctx, bin, filepath.Join(dir, "relaunch"), root, relaunchScenario(scenario))
	if e != nil {
		return e
	}
	defer func() { child.Process.Kill(); <-childDone }()
	select {
	case e := <-childDone:
		if e != nil {
			return fmt.Errorf("fresh launch: %w", e)
		}
	case <-ctx.Done():
		return errors.New("fresh launch timed out")
	}
	if !exists(filepath.Join(dir, "relaunch", "joined")) {
		return errors.New("fresh launch missing joined-window receipt (see relaunch/process.log)")
	}
	return nil
}
