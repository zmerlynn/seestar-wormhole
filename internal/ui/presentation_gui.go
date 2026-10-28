//go:build gui

package ui

import (
	"context"
	"io"
	"log/slog"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func runPlatformUI(ctx context.Context, root string, out io.Writer, run runFunc) error {
	return runUIWithWindow(ctx, root, out, run, openBrowser, osKeychain{}, showWindow)
}

func showWindow(ctx context.Context, url string, quit func(), activate <-chan struct{}) error {
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
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title: "seestar-wormhole", Width: 760, Height: 700, URL: url,
	})
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
