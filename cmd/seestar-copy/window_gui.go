//go:build gui

package main

import (
	"context"
	_ "embed"
	"io"
	"log/slog"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/zmerlynn/seestar-copy/internal/app"
)

// icon is a placeholder, rendered from the page's icon.svg.
//
//go:embed icon.png
var icon []byte

// present shows the app in a native window. The dialogs it returns belong
// to that window and answer Cancel until it exists.
func present(out io.Writer) (func(context.Context, string, func()) error, *app.Native) {
	var window atomic.Pointer[application.WebviewWindow]
	var wails atomic.Pointer[application.App]
	native := &app.Native{
		ChooseFolder: func(dir string) (string, error) {
			a, w := wails.Load(), window.Load()
			if a == nil || w == nil {
				return "", nil
			}
			d := a.Dialog.OpenFile().CanChooseDirectories(true).CanChooseFiles(false).CanCreateDirectories(true).
				SetTitle("Folder for your images").AttachToWindow(w)
			if dir != "" {
				d.SetDirectory(dir)
			}
			return d.PromptForSingleSelection()
		},
		SaveFile: func(name string) (string, error) {
			a, w := wails.Load(), window.Load()
			if a == nil || w == nil {
				return "", nil
			}
			return a.Dialog.SaveFile().SetFilename(name).CanCreateDirectories(true).AttachToWindow(w).PromptForSingleSelection()
		},
		Alert: func(message string) {
			if a := wails.Load(); a != nil {
				a.Dialog.Error().SetTitle("seestar-copy").SetMessage(message).Show()
			}
		},
	}
	open := func(ctx context.Context, url string, quit func()) error {
		if err := checkWindowSandbox(); err != nil {
			return err
		}
		fix, err := newFixture(ctx, quit, out)
		if err != nil {
			return err
		}
		return runWindow(ctx, quit, func(c windowCallbacks) windowActions {
			o := application.Options{
				Name:                        "seestar-copy",
				Icon:                        icon,
				DisableDefaultSignalHandler: true,
				Assets:                      application.AssetOptions{Middleware: blockWindowAssets, DisableLogging: true},
				Logger:                      slog.New(slog.NewTextHandler(io.Discard, nil)),
				ErrorHandler:                windowErrorHandler,
				ShouldQuit:                  c.shouldQuit,
				OnShutdown:                  c.shutdown,
				Windows: application.WindowsOptions{WndProcInterceptor: func(_ uintptr, msg uint32, wParam, _ uintptr) (uintptr, bool) {
					return c.endSession(msg, wParam)
				}},
			}
			fix.configure(&o)
			a := application.New(o)
			options := application.WebviewWindowOptions{Title: "seestar-copy", Width: 760, Height: 700, URL: url}
			options.Linux.Icon = icon
			w := a.Window.NewWithOptions(options)
			w.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) { c.closing(e.Cancel) })
			a.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) { c.started() })
			fix.attach(a, w)
			wails.Store(a)
			window.Store(w)
			return windowActions{run: a.Run, quit: a.Quit}
		})
	}
	return open, native
}
