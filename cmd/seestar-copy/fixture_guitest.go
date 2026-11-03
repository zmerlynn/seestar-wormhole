//go:build gui && guitest

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// fixture drives the real window for the native check in CI, which builds
// it with -tags guitest; release builds have none. The app's telescope is
// a fake with two files: MyWorks/done.fit copies, and MyWorks/held.fit
// stalls mid-download until the copy is stopped. SEESTAR_COPY_GUITEST
// names the scenario:
//
//   - "close" holds that transfer, previews Free up space, opens the
//     confirm dialog and cancels it, then closes the window on macOS and
//     Linux, and leaves it to the harness's WM_CLOSE on Windows.
//   - "quit" holds it, then waits for the harness's Quit event on macOS.
//   - "hold" holds it and waits for the harness to stop the app.
//   - "inuse" runs beside a "hold" app on the same folder, checks that it
//     shows Folder in use and can't start copying, then closes as "close".
//
// SEESTAR_COPY_GUITEST_FOLDER is the settings' folder. The fixture prints
// "guitest: ready" once the page has passed, and a receipt from Wails'
// last shutdown hook once the app has stopped, after checking that a held
// transfer left no temporary file and the state lock is free.
type fixture struct {
	ctx      context.Context // the window's
	quit     func()
	out      io.Writer
	scenario string
	folder   string
	messages chan string   // from the page
	loaded   chan struct{} // closed once the page has loaded
	w        *application.WebviewWindow
	seq      int
}

// held is closed once the fake's download of held.fit has stalled.
var (
	held     = make(chan struct{})
	holdOnce = sync.OnceFunc(func() { close(held) })
)

func init() {
	files := fstest.MapFS{
		"MyWorks/done.fit": {Data: make([]byte, 1<<10), ModTime: time.Unix(1761700000, 0), Mode: 0o644},
		"MyWorks/held.fit": {Data: make([]byte, 64<<10), ModTime: time.Unix(1761700000, 0), Mode: 0o644},
	}
	testDial = func(context.Context, string) (remote.Session, error) {
		f := remote.NewFake(files)
		f.Faults = map[string]remote.Fault{"MyWorks/held.fit": {After: 1 << 10, Stall: holdOnce}}
		return f, nil
	}
}

func newFixture(ctx context.Context, quit func(), out io.Writer) (*fixture, error) {
	scenario := os.Getenv("SEESTAR_COPY_GUITEST")
	switch scenario {
	case "close", "quit", "hold", "inuse":
	default:
		return nil, errors.New(`this test build needs SEESTAR_COPY_GUITEST set to "close", "quit", "hold" or "inuse"`)
	}
	folder := os.Getenv("SEESTAR_COPY_GUITEST_FOLDER")
	if folder == "" {
		return nil, errors.New("this test build needs SEESTAR_COPY_GUITEST_FOLDER set to the settings' folder")
	}
	return &fixture{ctx: ctx, quit: quit, out: out, scenario: scenario, folder: folder, messages: make(chan string, 16), loaded: make(chan struct{})}, nil
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
			if f.scenario != "inuse" {
				if err := f.checkStopped(); err != nil {
					fmt.Fprintln(f.out, "guitest: FAIL:", err)
				}
			}
		default:
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
	if f.scenario == "inuse" {
		if err := f.inUse(ctx); err != nil {
			return err
		}
	} else {
		select {
		case <-held:
		case <-ctx.Done():
			return fmt.Errorf("the copy didn't start held.fit: %w", ctx.Err())
		}
		if err := f.probe(ctx, `check(el('c-title').textContent==='Copying 2 of 2 files' && el('c-flight').textContent.includes('MyWorks/held.fit'),
 {title:el('c-title').textContent,flight:el('c-flight').textContent})`); err != nil {
			return fmt.Errorf("the page didn't show the held transfer: %w", err)
		}
	}
	if err := f.probe(ctx, `(() => {
 const c=el('t-copy');
 if(!el('main').hidden && c.getAttribute('aria-selected')==='true') c.focus();
 return check(document.activeElement===c,{main:!el('main').hidden,copy:c.getAttribute('aria-selected'),focused:document.activeElement?.id});
})()`); err != nil {
		return fmt.Errorf("the page didn't show Copy: %w", err)
	}
	for _, k := range []struct{ key, want string }{
		{"ArrowRight", "browse"}, {"ArrowRight", "free"}, {"ArrowRight", "settings"}, {"ArrowRight", "copy"}, {"ArrowLeft", "settings"},
		{"Home", "copy"}, {"End", "settings"}, {"End", "settings"}, {"Home", "copy"},
	} {
		if err := f.probe(ctx, keyProbe(k.key, k.want)); err != nil {
			return fmt.Errorf("%s: %w", k.key, err)
		}
	}
	if f.scenario != "inuse" {
		if err := f.confirm(ctx); err != nil {
			return err
		}
	}
	fmt.Fprintln(f.out, "guitest: ready")
	if (f.scenario == "close" || f.scenario == "inuse") && runtime.GOOS != "windows" {
		f.w.Close()
	}
	return nil
}

// inUse checks that the page shows Folder in use, and still does after
// Start copying is pressed, which logs a second "copying stopped".
func (f *fixture) inUse(ctx context.Context) error {
	const shown = `el('c-title').textContent==='Another copy is already using this folder.' && !el('c-start').hidden`
	const stops = `[...el('c-log').children].filter(li=>/ copying stopped( |$)/.test(li.textContent)).length`
	if err := f.probe(ctx, `check(`+shown+` && `+stops+`===1,{title:el('c-title').textContent,stops:`+stops+`})`); err != nil {
		return fmt.Errorf("the page didn't show Folder in use: %w", err)
	}
	if err := f.probe(ctx, `(() => {
 if(!window.__guitestStart) {window.__guitestStart=true; el('c-start').click();}
 return check(`+shown+` && `+stops+`===2,{title:el('c-title').textContent,stops:`+stops+`});
})()`); err != nil {
		return fmt.Errorf("Start copying didn't show Folder in use again: %w", err)
	}
	return nil
}

// confirm waits until done.fit's copy is in the state, previews Free up
// space, opens the confirm dialog and cancels it, which must focus Delete
// again. It never deletes.
func (f *fixture) confirm(ctx context.Context) error {
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	for {
		records, err := state.Load(filepath.Join(base, "seestar-copy"), f.folder, nil)
		if err != nil {
			return err
		}
		if _, ok := records["MyWorks/done.fit"]; ok {
			break
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return fmt.Errorf("done.fit wasn't copied: %w", ctx.Err())
		}
	}
	if err := f.probe(ctx, `(() => {
 if(!window.__guitestFind) {window.__guitestFind=true; el('t-free').click(); el('x-find').click();}
 const go=el('x-go');
 return check(!go.hidden && !go.disabled && go.textContent==='Delete 1 file…',{title:el('x-title').textContent,go:go.textContent,hidden:go.hidden});
})()`); err != nil {
		return fmt.Errorf("Preview what to delete didn't show a preview: %w", err)
	}
	if err := f.probe(ctx, `(() => {
 if(!window.__guitestAsk) {window.__guitestAsk=true; el('x-go').click();}
 const d=el('x-confirm');
 return check(d.open && document.activeElement===el('x-ccancel'),{open:d.open,focused:document.activeElement?.id});
})()`); err != nil {
		return fmt.Errorf("Delete didn't open the confirm dialog: %w", err)
	}
	if err := f.probe(ctx, `(() => {
 if(!window.__guitestCancel) {window.__guitestCancel=true; el('x-ccancel').click();}
 const d=el('x-confirm');
 return check(!d.open && document.activeElement===el('x-go'),{open:d.open,focused:document.activeElement?.id});
})()`); err != nil {
		return fmt.Errorf("Cancel didn't close the dialog and focus Delete: %w", err)
	}
	return nil
}

// checkStopped checks, once the app has stopped, that the held transfer
// left no temporary file in the folder and that the state lock is free.
func (f *fixture) checkStopped() error {
	err := filepath.WalkDir(f.folder, func(p string, d fs.DirEntry, err error) error {
		if err == nil && plan.IsTempName(d.Name()) {
			return fmt.Errorf("a temporary file is left: %s", p)
		}
		return err
	})
	if err != nil {
		return err
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	st, err := state.Open(filepath.Join(base, "seestar-copy"), f.folder, nil)
	if err != nil {
		return fmt.Errorf("the state lock isn't free: %w", err)
	}
	return st.Close()
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
