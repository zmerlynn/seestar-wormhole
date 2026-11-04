package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func await(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(what + " did not happen")
	}
}

// TestWindowStops checks each way out of the window: it asks the app to
// stop, and neither shutdown nor the native quit happens before the app
// has stopped.
func TestWindowStops(t *testing.T) {
	for _, by := range []string{"quit", "close", "logout"} {
		t.Run(by, func(t *testing.T) {
			ctx, stopped := context.WithCancel(t.Context())
			defer stopped()
			requested, nativeQuit := make(chan struct{}, 4), make(chan struct{})
			err := runWindow(ctx, func() { requested <- struct{}{} }, func(c windowCallbacks) windowActions {
				return windowActions{quit: func() { close(nativeQuit) }, run: func() error {
					c.started()
					done := make(chan struct{})
					switch by {
					case "quit":
						if !c.shouldQuit() {
							t.Fatal("Quit was canceled")
						}
						go func() { c.shutdown(); close(done) }()
					case "close":
						canceled := false
						c.closing(func() { canceled = true })
						if !canceled {
							t.Fatal("the window closed before the app stopped")
						}
						close(done)
					case "logout":
						if code, swallow := c.endSession(0x16, 0); code != 0 || !swallow {
							t.Fatal("a canceled logout reached Wails")
						}
						select {
						case <-requested:
							t.Fatal("a canceled logout stopped the app")
						default:
						}
						if _, swallow := c.endSession(0x11, 1); swallow {
							t.Fatal("another message was swallowed")
						}
						go func() {
							if code, swallow := c.endSession(0x16, 1); code != 0 || !swallow {
								t.Error("the logout reached Wails")
							}
							close(done)
						}()
					}
					await(t, requested, "the stop request")
					select {
					case <-done:
						if by != "close" {
							t.Fatal("shutdown returned before the app stopped")
						}
					case <-nativeQuit:
						t.Fatal("the window quit before the app stopped")
					case <-time.After(20 * time.Millisecond):
					}
					stopped()
					await(t, done, "shutdown")
					await(t, nativeQuit, "the native quit")
					// Once the app has stopped, closing goes ahead.
					canceled := false
					c.closing(func() { canceled = true })
					if canceled {
						t.Error("a close after the app stopped was canceled")
					}
					return errors.New("ran")
				}}
			})
			if err == nil || err.Error() != "ran" {
				t.Errorf("runWindow = %v", err)
			}
		})
	}
}

// TestWindowStopsWithApp checks that the window quits when the app stops
// on its own, as on a signal, and that the worker waits for the window to
// start.
func TestWindowStopsWithApp(t *testing.T) {
	ctx, stopped := context.WithCancel(t.Context())
	stopped()
	nativeQuit := make(chan struct{})
	runWindow(ctx, func() {}, func(c windowCallbacks) windowActions {
		return windowActions{quit: func() { close(nativeQuit) }, run: func() error {
			select {
			case <-nativeQuit:
				t.Fatal("quit before the window started")
			case <-time.After(20 * time.Millisecond):
			}
			c.started()
			await(t, nativeQuit, "the native quit")
			return nil
		}}
	})
}

// TestWindowReturnErrorBeforeStart checks that a window that fails before
// it starts returns its error without a native quit.
func TestWindowReturnErrorBeforeStart(t *testing.T) {
	want := errors.New("no webview")
	called := false
	err := runWindow(t.Context(), func() {}, func(windowCallbacks) windowActions {
		return windowActions{quit: func() { called = true }, run: func() error { return want }}
	})
	if err != want || called {
		t.Errorf("runWindow = %v, quit called %v", err, called)
	}
}

// TestBlockWindowAssets checks that Wails' own asset server and runtime
// transport answer nothing, so every request goes to the app's server.
func TestBlockWindowAssets(t *testing.T) {
	h := blockWindowAssets(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the request reached Wails")
	}))
	for _, r := range []*http.Request{
		httptest.NewRequest("GET", "/index.html", nil),
		httptest.NewRequest("POST", "/wails/runtime", strings.NewReader(`{"object":0,"method":0}`)),
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", r.Method, r.URL, w.Code)
		}
	}
}
