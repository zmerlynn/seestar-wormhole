package ui

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func awaitWindow(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("native action did not run")
	}
}

func TestNativeWindowCallbacks(t *testing.T) {
	for _, trigger := range []string{"quit", "close", "session"} {
		t.Run(trigger, func(t *testing.T) {
			ctx, release := context.WithCancel(t.Context())
			defer release()
			requested := make(chan struct{}, 4)
			nativeQuit := make(chan struct{})
			err := runWindow(ctx, func() { requested <- struct{}{} }, nil, func(c windowCallbacks) windowActions {
				return windowActions{quit: func() { close(nativeQuit) }, run: func() error {
					c.started()
					shutdownDone := make(chan struct{})
					switch trigger {
					case "quit":
						if !c.shouldQuit() {
							t.Fatal("native termination canceled")
						}
					case "close":
						canceled := false
						c.closing(func() { canceled = true })
						if !canceled {
							t.Fatal("close bypassed joined release")
						}
					case "session":
						if code, swallow := c.endSession(0x16, 0); code != 0 || !swallow {
							t.Fatal("canceled session reached Wails")
						}
						select {
						case <-requested:
							t.Fatal("canceled session requested quit")
						default:
						}
						if _, swallow := c.endSession(0x11, 1); swallow {
							t.Fatal("unrelated message swallowed")
						}
					}
					if trigger != "session" {
						awaitWindow(t, requested)
					}
					go func() {
						if trigger == "session" {
							if code, swallow := c.endSession(0x16, 1); code != 0 || !swallow {
								t.Error("session message reached Wails")
							}
						} else {
							c.shutdown()
						}
						close(shutdownDone)
					}()
					awaitWindow(t, requested)
					select {
					case <-shutdownDone:
						t.Fatal("shutdown returned before release")
					case <-nativeQuit:
						t.Fatal("native quit before release")
					case <-time.After(20 * time.Millisecond):
					}
					release()
					awaitWindow(t, shutdownDone)
					awaitWindow(t, nativeQuit)
					canceled := false
					c.closing(func() { canceled = true })
					if canceled {
						t.Fatal("close canceled after release")
					}
					return nil
				}}
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeActivation(t *testing.T) {
	ctx, release := context.WithCancel(t.Context())
	defer release()
	activate, focused, nativeQuit := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	var calls []string
	err := runWindow(ctx, func() {}, activate, func(c windowCallbacks) windowActions {
		return windowActions{
			unminimise: func() { calls = append(calls, "unminimise") },
			show:       func() { calls = append(calls, "show") },
			focus:      func() { calls = append(calls, "focus"); close(focused) },
			quit:       func() { close(nativeQuit) },
			run: func() error {
				c.started()
				activate <- struct{}{}
				awaitWindow(t, focused)
				if !reflect.DeepEqual(calls, []string{"unminimise", "show", "focus"}) {
					t.Fatalf("activation changed layout or missed focus: %v", calls)
				}
				release()
				awaitWindow(t, nativeQuit)
				return nil
			},
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeRunReturns(t *testing.T) {
	for _, started := range []bool{false, true} {
		for _, runErr := range []error{nil, errors.New("startup failed")} {
			ctx, release := context.WithCancel(t.Context())
			quitCalls := make(chan struct{}, 1)
			err := runWindow(ctx, func() {}, nil, func(c windowCallbacks) windowActions {
				return windowActions{quit: func() { quitCalls <- struct{}{} }, run: func() error {
					if started {
						c.started()
					}
					return runErr
				}}
			})
			// runWindow joins its worker before returning, including startup failure.
			release()
			if !errors.Is(err, runErr) {
				t.Fatalf("Run error lost: %v", err)
			}
			select {
			case <-quitCalls:
				t.Fatal("worker survived Run return")
			default:
			}
		}
	}
}

func TestNativeCanceledBeforeStarted(t *testing.T) {
	ctx, release := context.WithCancel(t.Context())
	release()
	quitCalls := make(chan struct{})
	runWindow(ctx, func() {}, nil, func(c windowCallbacks) windowActions {
		return windowActions{quit: func() { close(quitCalls) }, run: func() error {
			select {
			case <-quitCalls:
				t.Fatal("Quit before ApplicationStarted")
			default:
			}
			c.started()
			awaitWindow(t, quitCalls)
			return nil
		}}
	})
}
