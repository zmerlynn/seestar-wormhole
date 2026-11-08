//go:build gui && guitest

package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/guitest"
)

// The copy scenarios run the real Copy and Free up space tabs against a
// fake telescope with two files: MyWorks/done.fit copies, and
// MyWorks/held.fit stalls mid-download until copying stops. The route is
// this network's, at an address that is never dialed, and copying starts
// when the app opens.
//
//   - copy-close holds that transfer, presses the tab keys, previews Free
//     up space, opens the confirm dialog and cancels it, then closes the
//     window (the harness sends WM_CLOSE on Windows).
//   - copy-quit does the same and waits for the harness's Quit event.
//   - copy-hold holds it and waits for the harness to stop the app.
//   - copy-inuse runs beside a copy-hold app on the same folder, checks
//     that Copy shows the folder in use and Start copying can't take it,
//     then closes as copy-close.
//   - copy-save does as copy-close, and in Browse saves done.fit through
//     the window's save dialog, which the fixture answers with a file in
//     its output folder, then saves it again and cancels.
//   - copy-relaunch is the harness's fresh launch after any of them, on
//     the files they left: the copy tabs open with the saved settings,
//     copying starts on its own, and Quit stops it.
//
// Each marks "trigger" once its page has passed. The harness then checks
// that the held transfer left no temporary file and the folder's state
// lock is free.
var (
	copyHeld     = make(chan struct{})
	copyHoldOnce = sync.OnceFunc(func() { close(copyHeld) })
)

// copySaves answers copy-save's save dialogs: the first saves into its
// output folder and the second is canceled. Its alerts are failures.
var copySaves struct {
	sync.Mutex
	dir    string
	names  []string
	alerts []string
}

func copySaveFile(name string) (string, error) {
	copySaves.Lock()
	defer copySaves.Unlock()
	copySaves.names = append(copySaves.names, name)
	if len(copySaves.names) == 1 {
		return filepath.Join(copySaves.dir, "saved", name), nil
	}
	return "", nil
}

func copySaveAlert(message string) {
	copySaves.Lock()
	defer copySaves.Unlock()
	copySaves.alerts = append(copySaves.alerts, message)
}

func init() {
	files := fstest.MapFS{
		"MyWorks/done.fit": {Data: make([]byte, 1<<10), ModTime: time.Unix(1761700000, 0), Mode: 0o644},
		"MyWorks/held.fit": {Data: make([]byte, 64<<10), ModTime: time.Unix(1761700000, 0), Mode: 0o644},
	}
	copyDial = func(context.Context, copyapp.Target) (remote.Session, error) {
		f := remote.NewFake(files)
		f.Faults = map[string]remote.Fault{"MyWorks/held.fit": {After: 1 << 10, Stall: copyHoldOnce}}
		return f, nil
	}
}

// copyScenario reports whether scenario is one of the copy scenarios.
func copyScenario(scenario string) bool { return strings.HasPrefix(scenario, "copy-") }

// prepareCopyScenario saves a route on this network, established as a
// tested one is so the copy tabs open at launch, and copy settings that
// start copying when the app opens, through the app's own storage.
// The folder is beside the state dir, so two apps the harness starts with
// sibling state dirs share it.
func prepareCopyScenario(root string) error {
	st, err := openStorage(root)
	if err != nil {
		return err
	}
	defer st.directory.Close()
	settings := defaultSettings()
	settings.Route, settings.LANAddress, settings.RouteEstablished = routeLAN, "192.0.2.20", true
	if err := st.write("config.json", settings); err != nil {
		return err
	}
	set := copyapp.DefaultSettings()
	set.Folder = guitest.CopyFolder(root)
	set.StartCopying = true
	return (&copyStore{storage: st}).save(set)
}

func (d *guiTestDriver) driveCopy() error {
	if err := d.probe(`check(!el('tab-copy').disabled && el('tabs-note').hidden,{disabled:el('tab-copy').disabled})`); err != nil {
		return fmt.Errorf("the copy tabs didn't open: %w", err)
	}
	if d.scenario == "copy-relaunch" {
		return d.copyRelaunch()
	}
	d.js(`selectTab('copy');document.getElementById('tab-copy').focus();`)
	if err := d.probe(`check(!el('panel-copy').hidden && document.activeElement===el('tab-copy') && !el('c-main').hidden,{shown:!el('panel-copy').hidden,focused:document.activeElement?.id})`); err != nil {
		return fmt.Errorf("Copy didn't open: %w", err)
	}
	if d.scenario == "copy-inuse" {
		if err := d.copyInUse(); err != nil {
			return err
		}
	} else {
		select {
		case <-copyHeld:
		case <-d.ctx.Done():
			return errors.New("the copy didn't start held.fit")
		}
		if err := d.probe(`check(el('c-title').textContent==='Copying 2 of 2 files' && el('c-flight').textContent.includes('MyWorks/held.fit'),{title:el('c-title').textContent,flight:el('c-flight').textContent})`); err != nil {
			return fmt.Errorf("Copy didn't show the held transfer: %w", err)
		}
		if err := d.capture(d.ctx, filepath.Join(d.dir, "copying.png")); err != nil {
			return err
		}
	}
	if d.scenario == "copy-hold" {
		return d.mark("trigger")
	}
	if d.scenario != "copy-inuse" {
		// Browse's frame lists the fake telescope's top folder.
		d.js(`selectTab('browse');`)
		if err := d.probe(`check(!!el('b-frame').contentDocument?.body?.textContent.includes('MyWorks'),{src:el('b-frame').getAttribute('src'),text:(el('b-frame').contentDocument?.body?.textContent ?? '').slice(0,200)})`); err != nil {
			return fmt.Errorf("Browse didn't load: %w", err)
		}
		if err := d.capture(d.ctx, filepath.Join(d.dir, "browse.png")); err != nil {
			return err
		}
		if d.scenario == "copy-save" {
			if err := d.copySave(); err != nil {
				return err
			}
		}
		d.js(`selectTab('copy');`)
	}
	d.js(`document.getElementById('tab-copy').focus();`)
	for _, k := range []struct{ key, want string }{
		{"ArrowRight", "browse"}, {"ArrowRight", "free"}, {"ArrowRight", "activity"}, {"ArrowRight", "connection"},
		{"ArrowLeft", "activity"}, {"Home", "connection"}, {"End", "activity"}, {"End", "activity"}, {"ArrowRight", "connection"},
	} {
		if err := d.probe(copyKeyProbe(k.key, k.want)); err != nil {
			return fmt.Errorf("%s: %w", k.key, err)
		}
	}
	if d.scenario != "copy-inuse" {
		if err := d.copyConfirm(); err != nil {
			return err
		}
	}
	if err := d.mark("trigger"); err != nil {
		return err
	}
	if d.scenario == "copy-close" || d.scenario == "copy-inuse" || d.scenario == "copy-save" {
		if runtime.GOOS != "windows" {
			d.w.Close()
		}
	}
	return nil
}

// copyRelaunch checks that the settings the first launch saved are read
// back and that copying starts without a press, then quits.
func (d *guiTestDriver) copyRelaunch() error {
	folder := strconv.Quote(guitest.CopyFolder(d.root))
	if err := d.probe(`check(copySaved?.folder===` + folder + ` && copySaved.start_copying===true && !el('c-main').hidden && el('s-summary').textContent.endsWith('Starts when the app opens.'),{saved:copySaved,summary:el('s-summary').textContent})`); err != nil {
		return fmt.Errorf("Copy didn't read the saved settings back: %w", err)
	}
	select {
	case <-copyHeld:
	case <-d.ctx.Done():
		return errors.New("copying didn't start when the app opened")
	}
	if err := d.probe(`check(el('c-title').textContent.startsWith('Copying ') && el('c-flight').textContent.includes('MyWorks/held.fit'),{title:el('c-title').textContent,flight:el('c-flight').textContent})`); err != nil {
		return fmt.Errorf("Copy didn't show copying: %w", err)
	}
	return d.click("quit")
}

// copyInUse checks that Copy shows the folder in use, and still does
// after Start copying is pressed.
func (d *guiTestDriver) copyInUse() error {
	const shown = `el('c-title').textContent==='Another copy is already using this folder.' && !el('c-start').hidden`
	if err := d.probe(`check(` + shown + `,{title:el('c-title').textContent,start:!el('c-start').hidden})`); err != nil {
		return fmt.Errorf("Copy didn't show the folder in use: %w", err)
	}
	if err := d.capture(d.ctx, filepath.Join(d.dir, "inuse.png")); err != nil {
		return err
	}
	if err := d.click("c-start"); err != nil {
		return err
	}
	if err := d.probe(`check(` + shown + `,{title:el('c-title').textContent,start:!el('c-start').hidden})`); err != nil {
		return fmt.Errorf("Start copying took the folder in use: %w", err)
	}
	return nil
}

// copySave opens MyWorks in Browse and presses Save on done.fit twice:
// the first save must write its bytes where the dialog said, and the
// canceled one nothing.
func (d *guiTestDriver) copySave() error {
	saved := filepath.Join(d.dir, "saved")
	if err := os.MkdirAll(saved, 0o700); err != nil {
		return err
	}
	if err := d.probe(copyFrameClick("MyWorks/")); err != nil {
		return fmt.Errorf("Browse didn't open MyWorks: %w", err)
	}
	if err := d.probe(copyFrameClick("Save done.fit")); err != nil {
		return fmt.Errorf("Browse didn't save done.fit: %w", err)
	}
	if err := guitest.Wait(d.ctx, "done.fit saved", func() bool {
		b, err := os.ReadFile(filepath.Join(saved, "done.fit"))
		return err == nil && bytes.Equal(b, make([]byte, 1<<10))
	}); err != nil {
		return err
	}
	if err := d.probe(copyFrameClick("Save done.fit")); err != nil {
		return fmt.Errorf("Browse didn't save done.fit again: %w", err)
	}
	dialogs := func() int {
		copySaves.Lock()
		defer copySaves.Unlock()
		return len(copySaves.names)
	}
	if err := guitest.Wait(d.ctx, "the canceled save dialog", func() bool { return dialogs() == 2 }); err != nil {
		return err
	}
	// The canceled save answers once its dialog returns; Browse must still
	// show the folder after it.
	if err := d.probe(`check(!!el('b-frame').contentDocument?.body?.textContent.includes('done.fit'),{text:(el('b-frame').contentDocument?.body?.textContent ?? '').slice(0,200)})`); err != nil {
		return fmt.Errorf("Browse left the folder after a save: %w", err)
	}
	names, err := os.ReadDir(saved)
	if err != nil {
		return err
	}
	copySaves.Lock()
	defer copySaves.Unlock()
	if len(names) != 1 || strings.Join(copySaves.names, ",") != "done.fit,done.fit" || len(copySaves.alerts) != 0 {
		return fmt.Errorf("saves: dialogs %q, files %v, alerts %q", copySaves.names, names, copySaves.alerts)
	}
	return nil
}

// copyFrameClick clicks, once, the Browse frame's link with text or label
// name.
func copyFrameClick(name string) string {
	return `(() => {
 const name=` + strconv.Quote(name) + `,doc=el('b-frame').contentDocument;
 const a=doc && [...doc.querySelectorAll('a')].find(a=>a.textContent===name || a.getAttribute('aria-label')===name);
 if(!a) return check(false,{name,text:(doc?.body?.textContent ?? '').slice(0,200)});
 if((window.__guitestFrameClick ?? 0)<probe) {window.__guitestFrameClick=probe;a.click();}
 return true;
})()`
}

// copyConfirm waits until done.fit is copied, previews Free up space,
// opens the confirm dialog and cancels it, which must focus Delete again.
// It never deletes.
func (d *guiTestDriver) copyConfirm() error {
	if err := guitest.Wait(d.ctx, "done.fit copied", func() bool { return guitest.CopyRecorded(guitest.CopyFolder(d.root), "MyWorks/done.fit") }); err != nil {
		return err
	}
	d.js(`selectTab('free');`)
	if err := d.click("x-find"); err != nil {
		return err
	}
	if err := d.probe(`(() => {const go=el('x-go');return check(!go.hidden && !go.disabled && go.textContent==='Delete 1 file…',{title:el('x-title').textContent,go:go.textContent,hidden:go.hidden})})()`); err != nil {
		return fmt.Errorf("Preview what to delete didn't show a preview: %w", err)
	}
	if err := d.capture(d.ctx, filepath.Join(d.dir, "free-preview.png")); err != nil {
		return err
	}
	if err := d.click("x-go"); err != nil {
		return err
	}
	if err := d.probe(`check(el('x-confirm').open && document.activeElement===el('x-ccancel'),{open:el('x-confirm').open,focused:document.activeElement?.id})`); err != nil {
		return fmt.Errorf("Delete didn't open the confirm dialog: %w", err)
	}
	if err := d.click("x-ccancel"); err != nil {
		return err
	}
	if err := d.probe(`check(!el('x-confirm').open && document.activeElement===el('x-go'),{open:el('x-confirm').open,focused:document.activeElement?.id})`); err != nil {
		return fmt.Errorf("Cancel didn't close the dialog and focus Delete: %w", err)
	}
	return nil
}

// copyKeyProbe presses key on the focused tab, once, and checks that the
// tab want is then the one selected, focused and shown.
func copyKeyProbe(key, want string) string {
	return `(() => {
 const key=` + strconv.Quote(key) + `,want='tab-'+` + strconv.Quote(want) + `;
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
