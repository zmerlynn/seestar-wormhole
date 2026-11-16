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
	"github.com/zmerlynn/seestar-wormhole/internal/localhttp"
)

// Ignore the real runner entirely. Missing fixture configuration fails closed.
func runPlatformUI(ctx context.Context, root string, out, logs io.Writer, _ runFunc) error {
	dir, scenario := os.Getenv("SEESTAR_GUITEST_OUT"), os.Getenv("SEESTAR_GUITEST_SCENARIO")
	if dir == "" || scenario == "" {
		return errors.New("guitest requires its Go harness")
	}
	// The driver runs its probes and scripts in the first tab's frame through
	// eval, which the pages' script-src must allow. Only this build does.
	pagePolicy = strings.Replace(localhttp.Policy, "script-src 'self'", "script-src 'self' 'unsafe-eval'", 1)
	// copy-relaunch reads what the launch before it left.
	if copyScenario(scenario) && scenario != "copy-relaunch" {
		if err := prepareCopyScenario(root); err != nil {
			return err
		}
	}
	// The window's own dialogs, except copy-save's, which answer without
	// one; the browser has none.
	var dialogs windowDialogs
	native := dialogs.native()
	if scenario == "copy-save" {
		copySaves.dir = dir
		native.SaveFile, native.Alert = copySaveFile, copySaveAlert
	} else if scenario == "browser-copy" {
		native = nil
	}
	f := newGUITestRunner()
	f.scenario = scenario
	result := make(chan error, 1)
	started := false
	err := runUIPresented(ctx, root, out, logs, f.run, func(string) error { panic("guitest: browser opener") }, &guiTestKeychain{root: root, scenario: scenario}, func(windowCtx context.Context, url string, quit func(), activate <-chan struct{}) error {
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
			dialogs.attach(app, w)
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
			// Wails on Windows injects options.JS only into inline-HTML windows.
			if runtime.GOOS == "windows" {
				w.OnWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) { w.ExecJS(guiTestWindowOptions(scenario).JS) })
			}
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
	}, native)
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
// Fixture captures use a stable display path. The Go driver independently
// verifies the actual status path against its disposable state root. The
// first tab's page fetches its status in the shell's frame: wrap each page
// the frame loads, not its first blank page. The wrapper is the shell's
// function, so it calls the page's fetch on the page's window: called on
// the shell's, it resolves the page's relative paths against the shell's.
// Each page records its own focus for the probes' diagnostics.
const framePage=win => {
 if(!win.location.pathname.startsWith('/p/') || win.document.__guitestPage) return;
 win.document.__guitestPage=true;
 const fixtureFetch=win.fetch;
 win.fetch=async function(...args) {
  const response=await fixtureFetch.apply(win,args);
  if(!response.ok || new URL(args[0],win.location.href).pathname!=='/p/1/api/status') return response;
  const status=await response.json();
  return new win.Response(JSON.stringify({...status,log_path:'state/gui.log'}),{status:response.status,headers:response.headers});
 };
 const trace=win.__guitestFocus=[];
 const record=via => {if(trace.length<16) trace.push({via,focused:win.document.activeElement?.id,documentFocused:win.document.hasFocus(),ready:win.document.readyState})};
 record('page-loaded');
 win.addEventListener('focus', () => record('window-focus'));
 win.document.addEventListener('focusin', () => record('focusin'));
};
const framed=frame => {try {framePage(frame.contentWindow)} catch(_) {}};
document.querySelectorAll('iframe').forEach(framed);
document.addEventListener('load',e => {if(e.target.tagName==='IFRAME') framed(e.target)},true);
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
