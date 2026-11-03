//go:build gui && guitest

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// fixture drives the real window for the native check in CI, which builds
// it with -tags guitest; release builds have none. SEESTAR_COPY_GUITEST
// says how the window is left once the page passes: "close" closes it on
// macOS and Linux, and leaves it to the harness's WM_CLOSE on Windows;
// "quit" waits for the harness's Quit event on macOS. It prints
// "guitest: ready" once the page has passed, and a receipt from Wails'
// last shutdown hook once the app has stopped.
type fixture struct {
	ctx      context.Context // the window's
	quit     func()
	out      io.Writer
	scenario string
	messages chan string   // from the page
	loaded   chan struct{} // closed once the page has loaded
	w        *application.WebviewWindow
	seq      int
}

func newFixture(ctx context.Context, quit func(), out io.Writer) (*fixture, error) {
	scenario := os.Getenv("SEESTAR_COPY_GUITEST")
	if scenario != "close" && scenario != "quit" {
		return nil, errors.New(`this test build needs SEESTAR_COPY_GUITEST set to "close" or "quit"`)
	}
	return &fixture{ctx: ctx, quit: quit, out: out, scenario: scenario, messages: make(chan string, 16), loaded: make(chan struct{})}, nil
}

// configure takes the page's answers from the webview's own message
// bridge, which needs no Wails runtime in the page.
func (f *fixture) configure(o *application.Options) {
	o.RawMessageHandler = func(_ application.Window, m string, _ *application.OriginInfo) {
		if m, ok := strings.CutPrefix(m, "guitest:"); ok {
			select {
			case f.messages <- m:
			default:
			}
		}
	}
}

func (f *fixture) attach(a *application.App, w *application.WebviewWindow) {
	f.w = w
	// Wails holds ExecJS until its runtime script says it is ready, and
	// the app's page doesn't load it; say so once the page has loaded.
	loadEvent := map[string]events.WindowEventType{"darwin": events.Mac.WebViewDidFinishNavigation, "windows": events.Windows.WebViewNavigationCompleted, "linux": events.Linux.WindowLoadFinished}[runtime.GOOS]
	once := sync.OnceFunc(func() { close(f.loaded) })
	w.OnWindowEvent(loadEvent, func(*application.WindowEvent) {
		w.HandleMessage("wails:runtime:ready")
		once()
	})
	// This runs after the window's own shutdown hook, which waits for the
	// app to stop, and before AppKit may end the process.
	a.OnShutdown(func() {
		select {
		case <-f.ctx.Done():
			fmt.Fprintln(f.out, "guitest: the app stopped before the window quit")
		case <-time.After(5 * time.Second):
			fmt.Fprintln(f.out, "guitest: FAIL: the window quit before the app stopped")
		}
	})
	a.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			if err := f.drive(); err != nil {
				fmt.Fprintln(f.out, "guitest: FAIL:", err)
				f.quit()
			}
		}()
	})
}

func (f *fixture) drive() error {
	ctx, cancel := context.WithTimeout(f.ctx, 60*time.Second)
	defer cancel()
	select {
	case <-f.loaded:
	case <-ctx.Done():
		return fmt.Errorf("the page didn't load: %w", ctx.Err())
	}
	if err := f.probe(ctx, `(() => {
 const b=el('t-browse');
 if(!el('main').hidden && b.getAttribute('aria-selected')==='true') b.focus();
 return check(document.activeElement===b,{main:!el('main').hidden,browse:b.getAttribute('aria-selected'),focused:document.activeElement?.id});
})()`); err != nil {
		return fmt.Errorf("the page didn't show Browse: %w", err)
	}
	for _, k := range []struct{ key, want string }{
		{"ArrowRight", "settings"}, {"ArrowRight", "browse"}, {"ArrowLeft", "settings"},
		{"Home", "browse"}, {"End", "settings"}, {"End", "settings"}, {"Home", "browse"},
	} {
		if err := f.probe(ctx, keyProbe(k.key, k.want)); err != nil {
			return fmt.Errorf("%s: %w", k.key, err)
		}
	}
	fmt.Fprintln(f.out, "guitest: ready")
	if f.scenario == "close" && runtime.GOOS != "windows" {
		f.w.Close()
	}
	return nil
}

// keyProbe presses key on the focused tab, once, and checks that the tab
// want is then the one selected, focused and shown.
func keyProbe(key, want string) string {
	return `(() => {
 const key=` + strconv.Quote(key) + `,want='t-'+` + strconv.Quote(want) + `;
 if((window.__guitestKey ?? 0)<probe) {
  window.__guitestKey=probe;
  window.__guitestHandled=!document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key,bubbles:true,cancelable:true}));
 }
 const tabs=[...document.querySelectorAll('[role=tab]')];
 const got={key,handled:window.__guitestHandled,focused:document.activeElement?.id,
  selected:tabs.filter(t=>t.getAttribute('aria-selected')==='true').map(t=>t.id),
  tabIndex:tabs.map(t=>t.tabIndex),
  shown:[...document.querySelectorAll('[role=tabpanel]')].filter(p=>!p.hidden).map(p=>p.id)};
 return check(got.handled && got.focused===want && got.selected.join()===want &&
  tabs.every(t=>t.tabIndex===(t.id===want ? 0 : -1)) && got.shown.join()===el(want).getAttribute('aria-controls'),got);
})()`
}

// probe evaluates expr in the page until it is true, which the page
// acknowledges with this probe's own number, so a late answer to an
// earlier probe can't pass it. expr can use el, check and probe.
func (f *fixture) probe(ctx context.Context, expr string) error {
	f.seq++
	n := strconv.Itoa(f.seq)
	js := `(() => {
 const send=m=>{if(window.chrome?.webview) window.chrome.webview.postMessage(m); else window.webkit?.messageHandlers?.external?.postMessage(m)};
 const el=id=>document.getElementById(id),check=(ok,detail)=>ok ? true : detail,probe=` + n + `;
 try {const r=(` + expr + `);send(r===true ? 'guitest:ack:'+probe : 'guitest:probe '+probe+': '+JSON.stringify(r))}
 catch(e) {send('guitest:probe '+probe+': '+String(e))}
})()`
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	last := "no answer from the page"
	for {
		f.w.ExecJS(js)
	wait:
		for {
			select {
			case m := <-f.messages:
				if m == "ack:"+n {
					return nil
				}
				last = m
			case <-tick.C:
				break wait
			case <-ctx.Done():
				return fmt.Errorf("%s: %w", last, ctx.Err())
			}
		}
	}
}
