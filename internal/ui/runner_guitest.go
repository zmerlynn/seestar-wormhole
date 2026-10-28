//go:build gui && guitest

package ui

import (
	"context"
	"errors"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// No sockets, cloud adapter, ports, or OS credential provider exist in this
// runner. Progress is gated by the driver so each rendered state is observed.
type guiTestRunner struct {
	steps     chan session.Event
	entered   chan struct{}
	releasing chan struct{}
	released  chan struct{}
}

func newGUITestRunner() *guiTestRunner {
	return &guiTestRunner{make(chan session.Event), make(chan struct{}, 8), make(chan struct{}, 8), make(chan struct{}, 8)}
}

func (f *guiTestRunner) run(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status) error {
	f.entered <- struct{}{}
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

type guiTestKeychain struct{}

func (guiTestKeychain) Get(string, string) (string, error) { panic("guitest: keychain read") }
func (guiTestKeychain) Set(string, string, string) error   { panic("guitest: keychain write") }
func (guiTestKeychain) Delete(string, string) error        { panic("guitest: keychain delete") }
