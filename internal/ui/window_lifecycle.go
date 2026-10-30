package ui

import "context"

// Keep native calls at one boundary so headless tests exercise the callbacks
// and worker used by the actual presenter.
type windowActions struct {
	run                           func() error
	setURL                        func(string)
	quit, unminimise, show, focus func()
}
type windowCallbacks struct {
	shouldQuit        func() bool
	shutdown, started func()
	closing           func(cancel func())
	endSession        func(uint32, uintptr) (uintptr, bool)
}

func runWindow(ctx context.Context, url string, quit func(), activate <-chan struct{}, create func(windowCallbacks) windowActions) error {
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
			// A canceled session end must never reach Wails' unconditional App.Quit.
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
		for {
			select {
			case <-returned:
				return
			case <-ctx.Done():
				actions.quit()
				return
			case <-activate:
				actions.unminimise()
				actions.show()
				actions.setURL(url)
				actions.focus()
			}
		}
	}()
	err := actions.run()
	close(returned)
	// A synchronous native call may be stranded after Run stops its dispatcher.
	// The caller still joins Go release; it must not wait for this native worker.
	return err
}
