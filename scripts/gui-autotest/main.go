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
	scenarios := flag.String("scenarios", "fresh,quit-connecting,quit-ready,close-connecting,close-ready,activate,errors,term,interrupt", "comma-separated scenarios")
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
		err := runScenario(ctx, bin, filepath.Join(dir, s), s, true)
		cancel()
		if err != nil {
			reason := err.Error()
			if b, e := os.ReadFile(filepath.Join(dir, s, "process.log")); e == nil {
				if i := strings.LastIndex(string(b), "GUI fixture FAIL:"); i >= 0 {
					reason += "; " + strings.SplitN(string(b)[i:], "\n", 2)[0]
				}
			}
			fmt.Printf("SCENARIO %s FAIL: %s\n", s, reason)
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

func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func launch(ctx context.Context, bin, dir, root, scenario string) (*exec.Cmd, <-chan error, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, nil, err
	}
	log, err := os.Create(filepath.Join(dir, "process.log"))
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "ui", "--state-dir", root)
	// No user config, account, password provider, proxy ports, or network endpoint.
	cmd.Env = append(os.Environ(), "SEESTAR_GUITEST_OUT="+dir, "SEESTAR_GUITEST_SCENARIO="+scenario)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); log.Close(); done <- err; close(done) }()
	return cmd, done, nil
}

func runScenario(ctx context.Context, bin, dir, scenario string, relaunch bool) error {
	// A rerun gets fresh state and cannot accidentally use a previous checkpoint.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	root := filepath.Join(dir, "state")
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
			return exists(filepath.Join(dir, name))
		})
		return errors.Join(err, early)
	}
	// Read only the ephemeral listener address; never log its capability token.
	if err := waitFile("state/ui-runtime.json"); err != nil {
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
	if scenario == "activate" || scenario == "term" || scenario == "interrupt" || scenario == "apple" || (runtime.GOOS == "windows" && strings.HasPrefix(scenario, "close-")) {
		if err := waitFile("trigger"); err != nil {
			return err
		}
		switch scenario {
		case "activate":
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
	if !exists(filepath.Join(dir, "joined")) {
		return errors.New("missing joined-window receipt")
	}
	if scenario != "relaunch" && scenario != "errors" && !exists(filepath.Join(dir, "release-wait-observed")) {
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
	if relaunch {
		child, childDone, e := launch(ctx, bin, filepath.Join(dir, "relaunch"), root, "relaunch")
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
			return errors.New("fresh launch did not acquire lock/create native window")
		}
	}
	return nil
}
