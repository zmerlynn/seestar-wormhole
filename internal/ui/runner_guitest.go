//go:build gui && guitest

package ui

import (
	"context"
	"errors"
	"github.com/zalando/go-keyring"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/guitest"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// No sockets, real cloud transport, ports, or OS credential provider exist in
// this runner. Progress is gated by the driver so each rendered state is observed.
type guiTestRunner struct {
	scenario  string
	steps     chan session.Event
	entered   chan struct{}
	releasing chan struct{}
	released  chan struct{}
}

func newGUITestRunner() *guiTestRunner {
	return &guiTestRunner{steps: make(chan session.Event), entered: make(chan struct{}, 8), releasing: make(chan struct{}, 8), released: make(chan struct{}, 8)}
}

func (f *guiTestRunner) run(ctx context.Context, settings Settings, _ cloud.Credentials, status *proxy.Status) error {
	f.entered <- struct{}{}
	switch f.scenario {
	case "bind-error":
		return &ActionError{Code: "bind", Field: "bind"}
	case "http-permission":
		return &ActionError{Code: "http_permission", Field: "ports"}
	case "http-conflict":
		return &ActionError{Code: "http_conflict", Field: "ports"}
	case "discovery-error":
		return &ActionError{Code: "discovery", Field: "discovery"}
	}
	if f.scenario == "telescope-picker" || f.scenario == "dark-telescope-picker" {
		device, err := guitest.SelectTelescope(ctx, cloudhttp.Device{Serial: settings.Serial, Model: settings.Model})
		if err != nil {
			return err
		}
		status.SetSelectedDevice(device)
	}
	status.MarkArmed()
	before := session.Snapshot{State: session.Dialing}
	var result error
	for {
		select {
		case <-ctx.Done():
			f.releasing <- struct{}{}
			// Model a bounded, slow Stop that must join even after cancellation.
			time.Sleep(8 * time.Second)
			f.released <- struct{}{}
			if result != nil {
				return result
			}
			if f.scenario == "release-uncertain" {
				return errors.Join(ctx.Err(), session.ErrStop)
			}
			return ctx.Err()
		case e := <-f.steps:
			after := before
			switch {
			case e.Kind == session.Progress:
				after.State = session.Ready
			case errors.Is(e.Err, session.ErrBusy):
				after.State = session.Backoff
			case e.Err != nil:
				after.State = session.Backoff
			}
			status.GUITestObserve(e, before, after)
			var terminal *diagnostic.Terminal
			if errors.As(e.Err, &terminal) {
				result = e.Err
			}
			before = after
		}
	}
}

// The fixture persists only a boolean marker. Its one fake password is compiled
// into the test build; no native keychain is ever accessed.
type guiTestKeychain struct {
	root, scenario string
	mu             sync.Mutex
	saves, removes int
}

func (f *guiTestKeychain) Get(string, string) (string, error) {
	if _, err := os.Stat(filepath.Join(f.root, "fixture-keychain")); err != nil {
		return "", keyring.ErrNotFound
	}
	return "synthetic-password", nil
}
func (f *guiTestKeychain) Set(string, string, string) error {
	f.mu.Lock()
	f.saves++
	n := f.saves
	f.mu.Unlock()
	if f.scenario == "save-denied" && n == 1 {
		return errors.New("synthetic denial")
	}
	if f.scenario == "save-timeout" && n == 1 {
		time.Sleep(4 * time.Second)
	}
	return os.WriteFile(filepath.Join(f.root, "fixture-keychain"), []byte("saved"), 0600)
}
func (f *guiTestKeychain) Delete(string, string) error {
	f.mu.Lock()
	f.removes++
	n := f.removes
	f.mu.Unlock()
	if f.scenario == "remove-denied" && n <= 2 {
		return errors.New("synthetic denial")
	}
	if f.scenario == "remove-timeout" && n == 1 {
		time.Sleep(4 * time.Second)
	}
	err := os.Remove(filepath.Join(f.root, "fixture-keychain"))
	if os.IsNotExist(err) {
		return keyring.ErrNotFound
	}
	return err
}
