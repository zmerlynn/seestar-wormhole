//go:build gui

package ui

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func windowPresenter(root string) windowFunc {
	return func(ctx context.Context, url string, quit func(), activate <-chan struct{}) error {
		return showWindowConfigured(ctx, root, url, quit, activate, application.WebviewWindowOptions{}, nil)
	}
}

// The optional configuration callback is used by the tagged GUI fixture.
func showWindowConfigured(ctx context.Context, root, url string, quit func(), activate <-chan struct{}, options application.WebviewWindowOptions, configure func(*application.App, *application.WebviewWindow)) (err error) {
	if err := checkWindowSandbox(); err != nil {
		return err
	}
	profile, cleanup, err := windowProfile(root)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, cleanup()) }()
	return runWindow(ctx, quit, activate, func(callbacks windowCallbacks) windowActions {
		app := application.New(application.Options{
			Name:                        "seestar-wormhole",
			DisableDefaultSignalHandler: true,
			Assets:                      application.AssetOptions{Middleware: blockWindowAssets, DisableLogging: true},
			Logger:                      slog.New(slog.NewTextHandler(io.Discard, nil)),
			ShouldQuit:                  callbacks.shouldQuit,
			OnShutdown:                  callbacks.shutdown,
			Windows: application.WindowsOptions{WebviewUserDataPath: profile, WndProcInterceptor: func(_ uintptr, msg uint32, wParam, _ uintptr) (uintptr, bool) {
				return callbacks.endSession(msg, wParam)
			}},
		})
		options.Title, options.Width, options.Height, options.URL = "seestar-wormhole", 760, 700, url
		window := app.Window.NewWithOptions(options)
		if configure != nil {
			configure(app, window)
		}
		window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) { callbacks.closing(event.Cancel) })
		app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) { callbacks.started() })
		return windowActions{run: app.Run, quit: app.Quit,
			unminimise: func() { window.UnMinimise() }, show: func() { window.Show() }, focus: func() { window.Focus() }}
	})
}
