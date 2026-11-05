//go:build gui

package ui

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/icon"
	"io"
	"log/slog"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
)

// windowPresenter shows the page in a native window, with the dialogs
// that belong to it.
func windowPresenter(root string, out io.Writer) (windowFunc, *copyapp.Native) {
	var d windowDialogs
	return func(ctx context.Context, url string, quit func(), activate <-chan struct{}) error {
		return showWindowConfigured(ctx, root, url, out, quit, activate, application.WebviewWindowOptions{}, nil, d.attach)
	}, d.native()
}

// windowDialogs are the window's folder picker, save dialog and alert.
// Each answers Cancel until the window exists.
type windowDialogs struct {
	app    atomic.Pointer[application.App]
	window atomic.Pointer[application.WebviewWindow]
}

func (d *windowDialogs) attach(app *application.App, window *application.WebviewWindow) {
	d.app.Store(app)
	d.window.Store(window)
}

func (d *windowDialogs) native() *copyapp.Native {
	return &copyapp.Native{
		ChooseFolder: func(dir string) (string, error) {
			a, w := d.app.Load(), d.window.Load()
			if a == nil || w == nil {
				return "", nil
			}
			dialog := a.Dialog.OpenFile().CanChooseDirectories(true).CanChooseFiles(false).CanCreateDirectories(true).
				SetTitle("Folder for your images").AttachToWindow(w)
			if dir != "" {
				dialog.SetDirectory(dir)
			}
			return dialog.PromptForSingleSelection()
		},
		SaveFile: func(name string) (string, error) {
			a, w := d.app.Load(), d.window.Load()
			if a == nil || w == nil {
				return "", nil
			}
			return a.Dialog.SaveFile().SetFilename(name).CanCreateDirectories(true).AttachToWindow(w).PromptForSingleSelection()
		},
		Alert: func(message string) {
			if a := d.app.Load(); a != nil {
				a.Dialog.Error().SetTitle("seestar-wormhole").SetMessage(message).Show()
			}
		},
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
			Description:                 aboutDescription(uiVersion(ctx)),
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
		// Preserve the fixture's diagnostics handler while owning external links.
		rawMessage := appOptions.RawMessageHandler
		var app *application.App
		appOptions.RawMessageHandler = func(window application.Window, message string, origin *application.OriginInfo) {
			openPageLink(message, app.Browser.OpenURL)
			if rawMessage != nil {
				rawMessage(window, message, origin)
			}
		}
		app = application.New(appOptions)
		menu := application.DefaultApplicationMenu()
		configureProjectMenu(menu, app.Menu.ShowAbout, app.Browser.OpenURL)
		app.Menu.Set(menu)
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
