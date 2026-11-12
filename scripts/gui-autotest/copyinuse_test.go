//go:build guitest && !windows

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/guitest"
)

// fakeFixture marks a test binary started by runScenario as the fixture.
const fakeFixture = "SEESTAR_GUITEST_FAKE_FIXTURE"

func TestMain(m *testing.M) {
	if os.Getenv(fakeFixture) != "" {
		os.Exit(fixture())
	}
	os.Exit(m.Run())
}

// fixture stands in for the copy scenarios' apps: copy-hold holds the
// shared folder's lock until SIGTERM, copy-inuse finds it held, and
// copy-relaunch must get it, as its copying starts when it opens.
func fixture() int {
	root, out := os.Args[len(os.Args)-1], os.Getenv("SEESTAR_GUITEST_OUT")
	folder := guitest.CopyFolder(root)
	stateDir, err := state.DefaultDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	joined := func() int {
		if err := os.WriteFile(filepath.Join(out, "joined"), nil, 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	switch os.Getenv("SEESTAR_GUITEST_SCENARIO") {
	case "copy-hold":
		st, err := state.Open(stateDir, folder, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM)
		if err := os.WriteFile(filepath.Join(out, "trigger"), nil, 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		<-stop
		if err := st.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return joined()
	case "copy-inuse":
		if guitest.CopyStopped(folder) == nil {
			fmt.Fprintln(os.Stderr, "the copy-hold app isn't holding the folder")
			return 1
		}
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer l.Close()
		if err := os.MkdirAll(root, 0700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		record := filepath.Join(root, "ui-runtime.json")
		if err := os.WriteFile(record, fmt.Appendf(nil, `{"port":%d}`, l.Addr().(*net.TCPAddr).Port), 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.Remove(record)
		// The harness polls for the record while this app lives.
		time.Sleep(time.Second)
		return joined()
	case "copy-relaunch":
		st, err := state.Open(stateDir, folder, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "copying didn't start when the app opened:", err)
			return 1
		}
		if err := st.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return joined()
	}
	fmt.Fprintln(os.Stderr, "unknown scenario")
	return 1
}

// TestCopyInUseReleasesBeforeRelaunch pins the order: the fresh launch
// starts copying into the shared folder, so copy-hold must stop first.
func TestCopyInUseReleasesBeforeRelaunch(t *testing.T) {
	t.Setenv(fakeFixture, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "copy-inuse")
	if err := runScenario(ctx, os.Args[0], dir, "copy-inuse"); err != nil {
		t.Fatal(err)
	}
}
