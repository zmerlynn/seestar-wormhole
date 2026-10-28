//go:build gui

package ui

import (
	"context"
	"io"
	"log/slog"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func showWindow(ctx context.Context, url string, quit func(), activate <-chan struct{}) error {
	return showWindowConfigured(ctx, url, quit, activate, application.WebviewWindowOptions{}, nil)
}

// The optional configuration callback is used by the tagged GUI fixture.
func showWindowConfigured(ctx context.Context, url string, quit func(), activate <-chan struct{}, options application.WebviewWindowOptions, configure func(*application.App, *application.WebviewWindow)) error {
	app := application.New(application.Options{
		Name: "seestar-wormhole",
		// The command owns signal cancellation and the proxy's joined release.
		DisableDefaultSignalHandler: true,
		Logger:                      slog.New(slog.NewTextHandler(io.Discard, nil)),
		ShouldQuit: func() bool {
			if ctx.Err() != nil {
				return true
			}
			quit()
			return false
		},
		OnShutdown: func() { quit(); <-ctx.Done() },
	})
	options.Title, options.Width, options.Height, options.URL = "seestar-wormhole", 760, 700, url
	window := app.Window.NewWithOptions(options)
	if configure != nil {
		configure(app, window)
	}
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if ctx.Err() == nil {
			event.Cancel()
			quit()
		}
	})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			for {
				select {
				case <-ctx.Done():
					app.Quit()
					return
				case <-activate:
					window.Restore()
					window.Show()
					window.Focus()
				}
			}
		}()
	})
	return app.Run()
}
