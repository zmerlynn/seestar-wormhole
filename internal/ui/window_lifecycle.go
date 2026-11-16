package ui

import (
	"context"
	"time"
)

// Keep native calls at one boundary so headless tests exercise the callbacks
// and worker used by the actual presenter.
type windowActions struct {
	run                           func() error
	setURL                        func(string)
	exec                          func(string)
	quit, unminimise, show, focus func()
}
type windowCallbacks struct {
	shouldQuit        func() bool
	shutdown, started func()
	closing           func(cancel func())
	endSession        func(uint32, uintptr) (uintptr, bool)
}

// windowShell is what the window asks the app of the shell page it shows:
// whether the shell asked for its tabs since a time, waiting briefly for
// it to, and whether quitting asks the shell to confirm first.
type windowShell struct {
	live    func(time.Time) bool
	confirm func() bool
}

type windowShellKey struct{}

// shellOf is the shell of the window under ctx; nil when the run has
// none, as the tagged GUI fixture's.
func shellOf(ctx context.Context) *windowShell {
	s, _ := ctx.Value(windowShellKey{}).(*windowShell)
	return s
}

// confirmQuitJS asks the shell to confirm quitting while a tab copies.
const confirmQuitJS = "typeof confirmQuit === 'function' && confirmQuit()"

func runWindow(ctx context.Context, url string, quit func(), activate <-chan struct{}, create func(windowCallbacks) windowActions) error {
	started, returned := make(chan struct{}, 1), make(chan struct{})
	shutdown := func() { quit(); <-ctx.Done() }
	shell := shellOf(ctx)
	var actions windowActions
	// ask asks the shell to confirm quitting, when it should: the shell
	// then quits, or doesn't.
	ask := func() bool {
		if ctx.Err() != nil || shell == nil || shell.confirm == nil || actions.exec == nil || !shell.confirm() {
			return false
		}
		actions.exec(confirmQuitJS)
		return true
	}
	callbacks := windowCallbacks{
		shouldQuit: func() bool {
			if ask() {
				return false
			}
			quit()
			return true
		},
		shutdown: shutdown,
		started: func() {
			select {
			case started <- struct{}{}:
			default:
			}
		},
		closing: func(cancel func()) {
			if ctx.Err() == nil {
				cancel()
				if !ask() {
					quit()
				}
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
	actions = create(callbacks)
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
				mark := time.Now()
				actions.unminimise()
				actions.show()
				// A shell that is live keeps its tabs; one that isn't, as
				// after the page was navigated away, is loaded again.
				if shell == nil || shell.live == nil || !shell.live(mark) {
					actions.setURL(url)
				}
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
