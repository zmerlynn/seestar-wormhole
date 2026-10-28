//go:build gui

package ui

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/assets/icon"
	"io"
	"log/slog"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func windowPresenter(root string, out io.Writer) windowFunc {
	return func(ctx context.Context, url string, quit func(), activate <-chan struct{}) error {
		return showWindowConfigured(ctx, root, url, out, quit, activate, application.WebviewWindowOptions{}, nil, nil)
	}
}

// The optional configuration callback is used by the tagged GUI fixture.
func showWindowConfigured(ctx context.Context, root, url string, out io.Writer, quit func(), activate <-chan struct{}, options application.WebviewWindowOptions, configureOptions func(*application.Options), configure func(*application.App, *application.WebviewWindow)) (err error) {
	if err := checkWindowSandbox(); err != nil {
		return err
	}
	profile, cleanup, err := windowProfile(root)
	if err != nil {
		return err
	}
	defer func() { err = profileCleanupResult(out, cleanup, err) }()
	return runWindow(ctx, url, quit, activate, func(callbacks windowCallbacks) windowActions {
		appOptions := application.Options{
			Name:                        "seestar-wormhole",
			Icon:                        icon.PNG,
			DisableDefaultSignalHandler: true,
			Assets:                      application.AssetOptions{Middleware: blockWindowAssets, DisableLogging: true},
			Logger:                      slog.New(slog.NewTextHandler(io.Discard, nil)),
			ErrorHandler:                windowErrorHandler,
			ShouldQuit:                  callbacks.shouldQuit,
			OnShutdown:                  callbacks.shutdown,
			Windows: application.WindowsOptions{WebviewUserDataPath: profile, WndProcInterceptor: func(_ uintptr, msg uint32, wParam, _ uintptr) (uintptr, bool) {
				return callbacks.endSession(msg, wParam)
			}},
		}
		if configureOptions != nil {
			configureOptions(&appOptions)
		}
		app := application.New(appOptions)
		options.Title, options.Width, options.Height, options.URL = "seestar-wormhole", 760, 700, url
		options.Linux.Icon = icon.PNG
		window := app.Window.NewWithOptions(options)
		if configure != nil {
			configure(app, window)
		}
		window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) { callbacks.closing(event.Cancel) })
		app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) { callbacks.started() })
		return windowActions{run: app.Run, quit: app.Quit, setURL: func(url string) { window.SetURL(url) },
			unminimise: func() { window.UnMinimise() }, show: func() { window.Show() }, focus: func() { window.Focus() }}
	})
}
