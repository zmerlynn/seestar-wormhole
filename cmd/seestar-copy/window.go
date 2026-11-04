package main

import (
	"context"
	"net/http"
)

// windowActions and windowCallbacks keep the native calls at one
// boundary, so the tests run the same callbacks and worker as the window.
type windowActions struct {
	run  func() error
	quit func()
}

type windowCallbacks struct {
	shouldQuit        func() bool
	shutdown, started func()
	closing           func(cancel func())
	endSession        func(msg uint32, confirmed uintptr) (uintptr, bool)
}

// runWindow runs the window that create makes until it quits. ctx is the
// app's window context, which ends only once the app has stopped; quit
// asks the app to stop. Every way out of the window asks the app to stop
// and waits for it: Quit and logout block in shutdown until ctx is done,
// and closing the window is canceled until then, so cleanup always
// finishes before the process can exit.
func runWindow(ctx context.Context, quit func(), create func(windowCallbacks) windowActions) error {
	started, returned := make(chan struct{}, 1), make(chan struct{})
	shutdown := func() { quit(); <-ctx.Done() }
	callbacks := windowCallbacks{
		shouldQuit: func() bool { quit(); return true },
		shutdown:   shutdown,
		started: func() {
			select {
			case started <- struct{}{}:
			default:
			}
		},
		closing: func(cancel func()) {
			if ctx.Err() == nil {
				cancel()
				quit()
			}
		},
		endSession: func(msg uint32, confirmed uintptr) (uintptr, bool) {
			const wmEndSession = 0x0016
			if msg != wmEndSession {
				return 0, false
			}
			if confirmed != 0 {
				shutdown()
			}
			// A canceled session end must never reach Wails, which quits
			// on any WM_ENDSESSION.
			return 0, true
		},
	}
	actions := create(callbacks)
	go func() {
		select {
		case <-returned:
			return
		case <-started:
		}
		select {
		case <-returned:
		case <-ctx.Done():
			actions.quit()
		}
	}()
	err := actions.run()
	close(returned)
	// A native call may be stranded once Run stops its dispatcher, so this
	// does not wait for the worker above.
	return err
}

// blockWindowAssets keeps every request on the app's own loopback server,
// which checks the token: Wails' asset server and runtime transport answer
// nothing.
func blockWindowAssets(http.Handler) http.Handler { return http.NotFoundHandler() }
