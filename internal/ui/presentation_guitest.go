//go:build gui && guitest

package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Ignore the real runner entirely. Missing fixture configuration fails closed.
func runPlatformUI(ctx context.Context, root string, out io.Writer, _ runFunc) error {
	dir, scenario := os.Getenv("SEESTAR_GUITEST_OUT"), os.Getenv("SEESTAR_GUITEST_SCENARIO")
	if dir == "" || scenario == "" {
		return errors.New("guitest requires its Go harness")
	}
	f := newGUITestRunner()
	f.scenario = scenario
	result := make(chan error, 1)
	started := false
	err := runUIPresented(ctx, root, out, f.run, func(string) error { panic("guitest: browser opener") }, &guiTestKeychain{root: root, scenario: scenario}, func(windowCtx context.Context, url string, quit func(), activate <-chan struct{}) error {
		started = true
		return showWindowConfigured(windowCtx, root, url, out, quit, activate, guiTestWindowOptions(scenario), func(options *application.Options) {
			options.RawMessageHandler = func(_ application.Window, message string, _ *application.OriginInfo) {
				if detail, ok := strings.CutPrefix(message, "guitest:diagnostic:"); ok && len(detail) <= 16384 {
					fmt.Fprintln(out, "DOM diagnostics:", detail)
				}
			}
			// Configure before New starts background logging or reports startup
			// errors. Preserve fixture warnings/errors without URL fragments.
			options.Logger = slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelWarn, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				if a.Value.Kind() == slog.KindString {
					text, _, _ := strings.Cut(a.Value.String(), "#")
					a.Value = slog.StringValue(text)
				}
				return a
			}}))
		}, func(app *application.App, w *application.WebviewWindow) {
			// AppKit may exit directly after NSTerminateNow, without returning Run.
			// Record the joined Go lifetime while OnShutdown still owns termination.
			app.OnShutdown(func() {
				<-windowCtx.Done()
				select {
				case driverErr := <-result:
					result <- driverErr
					if driverErr == nil {
						if err := os.WriteFile(filepath.Join(dir, "joined"), []byte("release and driver joined before native termination\n"), 0600); err != nil {
							fmt.Fprintln(out, "GUI fixture FAIL: join receipt", err)
						}
					}
				case <-time.After(5 * time.Second):
					fmt.Fprintln(out, "GUI fixture FAIL: driver did not join before native termination")
				}
			})
			// An external HTTP page does not import /wails/runtime.js, which normally
			// sends this readiness message. Release ExecJS only after native navigation
			// completes; the probes still require the real page and app.js to be ready.
			loaded := map[string]events.WindowEventType{"darwin": events.Mac.WebViewDidFinishNavigation, "windows": events.Windows.WebViewNavigationCompleted, "linux": events.Linux.WindowLoadFinished}[runtime.GOOS]
			w.OnWindowEvent(loaded, func(*application.WindowEvent) { w.HandleMessage("wails:runtime:ready") })
			app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
				go func() {
					driverCtx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
					defer cancel()
					d := &guiTestDriver{ctx: driverCtx, windowCtx: windowCtx, app: app, w: w, runner: f, url: url, dir: dir, scenario: scenario, root: root, out: out}
					driveErr := d.drive()
					if driveErr != nil {
						fmt.Fprintln(out, "GUI fixture FAIL:", driveErr)
						shotCtx, shotCancel := context.WithTimeout(context.Background(), 5*time.Second)
						if shotErr := d.capture(shotCtx, filepath.Join(dir, "failure.png")); shotErr != nil {
							fmt.Fprintln(out, "failure screenshot:", shotErr)
						}
						shotCancel()
						quit()
					}
					result <- driveErr
				}()
			})
		})
	}, scenario != "browser-copy")
	if started {
		select {
		case driverErr := <-result:
			err = errors.Join(err, driverErr)
		case <-time.After(5 * time.Second):
			err = errors.Join(err, errors.New("guitest driver did not join"))
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "joined"), []byte("window returned after release\n"), 0600)
		}
	}
	return err
}

// Set the native appearance inside the sandboxed fixture. Global defaults are
// written by the unsandboxed harness and may not reach AppKit across that boundary.
func guiTestWindowOptions(scenario string) application.WebviewWindowOptions {
	appearance := application.NSAppearanceNameAqua
	if strings.HasPrefix(scenario, "dark") {
		appearance = application.NSAppearanceNameDarkAqua
	}
	return application.WebviewWindowOptions{AllowSimpleEventEmit: true, Mac: application.MacWindow{Appearance: appearance}, JS: `(() => {
window.__guitestFocus = [];
const recordFocus = via => {
 if(window.__guitestFocus.length<16) window.__guitestFocus.push({via,focused:document.activeElement?.id,documentFocused:document.hasFocus(),ready:document.readyState});
};
recordFocus('navigation-finished');
const originalFocus = HTMLElement.prototype.focus;
HTMLElement.prototype.focus = function(...args) {recordFocus('focus-call:'+this.id);return originalFocus.apply(this,args)};
window.addEventListener('focus', () => recordFocus('window-focus'));
document.addEventListener('focusin', () => recordFocus('focusin'));
})()`}
}
