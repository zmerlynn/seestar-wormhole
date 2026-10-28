//go:build gui && guitest

package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"github.com/zmerlynn/seestar-wormhole/internal/guitest"
)

// Ignore the real runner entirely. Missing fixture configuration fails closed.
func runPlatformUI(ctx context.Context, root string, out io.Writer, _ runFunc) error {
	dir, scenario := os.Getenv("SEESTAR_GUITEST_OUT"), os.Getenv("SEESTAR_GUITEST_SCENARIO")
	if dir == "" || scenario == "" {
		return errors.New("guitest requires its Go harness")
	}
	f := newGUITestRunner()
	result := make(chan error, 1)
	started := false
	err := runUIWithWindow(ctx, root, out, f.run, func(string) error { panic("guitest: browser opener") }, guiTestKeychain{}, func(windowCtx context.Context, url string, quit func(), activate <-chan struct{}) error {
		started = true
		return showWindowConfigured(windowCtx, root, url, quit, activate, application.WebviewWindowOptions{AllowSimpleEventEmit: true}, func(app *application.App, w *application.WebviewWindow) {
			// An external HTTP page does not import /wails/runtime.js, which normally
			// sends this readiness message. Release ExecJS only after native navigation
			// completes; the probes still require the real page and app.js to be ready.
			loaded := map[string]events.WindowEventType{"darwin": events.Mac.WebViewDidFinishNavigation, "windows": events.Windows.WebViewNavigationCompleted, "linux": events.Linux.WindowLoadFinished}[runtime.GOOS]
			w.OnWindowEvent(loaded, func(*application.WindowEvent) { w.HandleMessage("wails:runtime:ready") })
			app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
				go func() {
					driverCtx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
					defer cancel()
					d := &guiTestDriver{ctx: driverCtx, windowCtx: windowCtx, app: app, w: w, runner: f, url: url, dir: dir, scenario: scenario}
					driveErr := d.drive()
					if driveErr != nil {
						fmt.Fprintln(out, "GUI fixture FAIL:", driveErr)
						shotCtx, shotCancel := context.WithTimeout(context.Background(), 5*time.Second)
						if shotErr := guitest.Capture(shotCtx, filepath.Join(dir, "failure.png")); shotErr != nil {
							fmt.Fprintln(out, "failure screenshot:", shotErr)
						}
						shotCancel()
						quit()
					}
					result <- driveErr
				}()
			})
		})
	})
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
