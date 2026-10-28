//go:build gui && guitest

package ui

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/guitest"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type guiTestDriver struct {
	ctx, windowCtx     context.Context
	app                *application.App
	w                  *application.WebviewWindow
	runner             *guiTestRunner
	url, dir, scenario string
	out                io.Writer
	seq                int
}

// Each probe has a distinct acknowledgment, so late JS cannot satisfy the next
// assertion. The native postMessage bridge needs no HTTP runtime or CSP change.
func (d *guiTestDriver) probe(expression string) error {
	d.seq++
	name := "guitest:probe:" + strconv.Itoa(d.seq)
	ack := make(chan struct{}, 1)
	off := d.app.Event.On(name, func(*application.CustomEvent) {
		select {
		case ack <- struct{}{}:
		default:
		}
	})
	defer off()
	var started, evaluated atomic.Bool
	offStarted := d.app.Event.On(name+":started", func(*application.CustomEvent) { started.Store(true) })
	offEvaluated := d.app.Event.On(name+":evaluated", func(*application.CustomEvent) { evaluated.Store(true) })
	defer offStarted()
	defer offEvaluated()
	js := `(() => {
 const send=m=>{if(window.chrome?.webview) window.chrome.webview.postMessage(m);
 else if(window.webkit?.messageHandlers?.external) window.webkit.messageHandlers.external.postMessage(m);
 else if(window.wails?.invoke) window.wails.invoke(m)};
 const diagnose=detail=>{if(window.__guitestDiagnostic!==detail){window.__guitestDiagnostic=detail;send('guitest:diagnostic:'+detail)}};
 send(` + strconv.Quote("wails:event:emit:"+name+":started") + `);
 try {const result=(` + expression + `);
 if(result===true) send(` + strconv.Quote("wails:event:emit:"+name) + `);
 else diagnose(typeof result==='string' ? result : JSON.stringify({result}));
 } catch(e) {diagnose(JSON.stringify({probeError:String(e)}))}
 finally {send(` + strconv.Quote("wails:event:emit:"+name+":evaluated") + `)}
 })()`
	err := guitest.Wait(d.ctx, "DOM probe "+strconv.Itoa(d.seq), func() bool {
		select {
		case <-ack:
			return true
		default:
			d.w.ExecJS(js)
			return false
		}
	})
	if err != nil {
		// Native evidence survives a broken bridge, a parse error, or a hung JS call.
		fmt.Fprintf(d.out, "DOM diagnostics: {\"probe\":%d,\"started\":%t,\"evaluated\":%t,\"timeout\":true}\n", d.seq, started.Load(), evaluated.Load())
	}
	return err
}

func (d *guiTestDriver) click(id string) error {
	if err := d.probe(`document.getElementById(` + strconv.Quote(id) + `)?.disabled === false`); err != nil {
		return err
	}
	d.w.ExecJS(`(() => {const e=document.getElementById(` + strconv.Quote(id) + `); e.scrollIntoView({block:'center'}); e.focus(); e.click();})()`)
	return nil
}

func (d *guiTestDriver) start() error {
	if err := d.click("start"); err != nil {
		return err
	}
	select {
	case <-d.runner.entered:
		return nil
	case <-d.ctx.Done():
		return errors.New("fake runner never entered Connecting")
	}
}

func (d *guiTestDriver) state(want string, active bool, shot string) error {
	if err := d.serverState(want, active); err != nil {
		return err
	}

	d.w.ExecJS(`window.scrollTo(0,0)`)
	if err := d.probe(`document.getElementById('status').textContent === ` + strconv.Quote(want) + ` && document.getElementById('start').disabled === ` + strconv.FormatBool(active) + ` && getComputedStyle(document.getElementById('status')).visibility === 'visible' && document.getElementById('status').getBoundingClientRect().top >= 0`); err != nil {
		return err
	}
	if strings.HasPrefix(d.scenario, "dark") {
		// Calculate WCAG luminance using the rendered CSS colors, without changing
		// the page's styles or emulating a media query.
		if err := d.probe(`(() => {
   const dark=matchMedia('(prefers-color-scheme: dark)').matches;
   const root=getComputedStyle(document.documentElement).backgroundColor;
   const canvas=document.createElement('canvas');canvas.width=canvas.height=1;
   const resolver=document.createElement('span');
   // Keep a rendered style context; hidden/display:none has no renderer.
   canvas.style.cssText=resolver.style.cssText='position:fixed;left:-2px;top:-2px;width:1px;height:1px;pointer-events:none';
   document.documentElement.append(canvas,resolver);
   try {
   const paint=canvas.getContext('2d',{willReadFrequently:true});
   // Resolve system colors as the element's used color, independent of canvas
   // platform-color parsing and color-mix support in the engine.
   const absolute=c=>{resolver.style.color='';resolver.style.color=c;
    if(!resolver.style.color) throw new Error('CSS color resolution failed: '+c);
    return getComputedStyle(resolver).color};
   const sample=(...colors)=>{paint.clearRect(0,0,1,1);for(const c of colors){paint.fillStyle=absolute(c);paint.fillRect(0,0,1,1)}
    return Array.from(paint.getImageData(0,0,1,1).data).slice(0,3)};
   const l=rgb=>{const v=rgb.map(n=>{n/=255;return n<=.04045?n/12.92:((n+.055)/1.055)**2.4});return .2126*v[0]+.7152*v[1]+.0722*v[2]};
   const rootRGB=sample(root),rootLuminance=l(rootRGB),failures=[];
   for(const id of ['status','email','password','password-note','start','stop','save','quit']){
    const e=document.getElementById(id),s=getComputedStyle(e);if(e.matches(':disabled')) continue;
    const fg=sample(root,s.backgroundColor,s.color),bg=sample(root,s.backgroundColor);
    const x=l(fg),y=l(bg),ratio=(Math.max(x,y)+.05)/(Math.min(x,y)+.05);
    if(ratio<3) failures.push({id,color:s.color,background:s.backgroundColor,fg,bg,ratio});
   }
   return dark && rootLuminance<.2 && failures.length===0 ? true : JSON.stringify({dark,root,rootRGB,rootLuminance,failures,colorMix:CSS.supports("color","color-mix(in srgb, red, blue)")});
   } finally { canvas.remove();resolver.remove(); }
  })()`); err != nil {
			return err
		}
	}
	// Give the compositor a frame after the DOM acknowledgment.
	time.Sleep(200 * time.Millisecond)
	path := filepath.Join(d.dir, shot+".png")
	if err := d.capture(d.ctx, path); err != nil {
		return err
	}
	if strings.HasPrefix(d.scenario, "dark") {
		return guitest.CheckDarkPixels(path)
	}
	return nil
}

func (d *guiTestDriver) step(e session.Event) error {
	select {
	case d.runner.steps <- e:
		return nil
	case <-d.ctx.Done():
		return d.ctx.Err()
	}
}
func (d *guiTestDriver) mark(name string) error {
	return os.WriteFile(filepath.Join(d.dir, name), []byte("ready\n"), 0600)
}
func (d *guiTestDriver) release() error {
	select {
	case <-d.runner.releasing:
	case <-d.ctx.Done():
		return errors.New("release never started")
	}
	time.Sleep(400 * time.Millisecond)
	if d.windowCtx.Err() != nil {
		return errors.New("window lifetime ended before release joined")
	}
	// Accepted AppKit termination joins OnShutdown on the native main thread.
	// Native queries/ExecJS must not wait on that thread during the join.
	if d.scenario != "apple" {
		if !d.w.IsVisible() {
			return errors.New("window closed before release joined")
		}
		if err := d.state("Stopping — releasing session", true, "releasing"); err != nil {
			return err
		}
	}
	if err := d.mark("release-wait-observed"); err != nil {
		return err
	}
	select {
	case <-d.runner.released:
		return nil
	case <-d.ctx.Done():
		return errors.New("release did not join")
	}
}

func (d *guiTestDriver) drive() error {
	if err := d.state("Stopped", false, "setup"); err != nil {
		return err
	}
	if d.scenario == "relaunch" {
		return d.click("quit")
	}
	if err := d.probe(`document.getElementById('email').value === '' && !document.getElementById('remember').checked`); err != nil {
		return err
	}
	d.w.ExecJS(`for(const [id,value] of [['email','gui@example.invalid'],['password','synthetic-password']]) {const e=document.getElementById(id);e.focus();e.value=value;e.dispatchEvent(new Event('input',{bubbles:true}));e.dispatchEvent(new Event('change',{bubbles:true}));}`)
	if err := d.start(); err != nil {
		return err
	}
	if err := d.state("Connecting", true, "connecting"); err != nil {
		return err
	}
	connecting := strings.Contains(d.scenario, "connecting")
	if !connecting {
		if err := d.step(session.Event{Kind: session.Progress}); err != nil {
			return err
		}
		if err := d.state("Ready", true, "ready"); err != nil {
			return err
		}
	}
	switch d.scenario {
	case "fresh", "dark":
		if err := d.click("stop"); err != nil {
			return err
		}
		if err := d.release(); err != nil {
			return err
		}
		if err := d.state("Stopped", false, "stopped"); err != nil {
			return err
		}
		if err := d.start(); err != nil {
			return err
		}
		if err := d.state("Connecting", true, "restart-connecting"); err != nil {
			return err
		}
		if err := d.step(session.Event{Kind: session.Progress}); err != nil {
			return err
		}
		if err := d.state("Ready", true, "restart-ready"); err != nil {
			return err
		}
	case "errors":
		for i, e := range []session.Event{{Kind: session.Failed, Err: session.ErrBusy}, {Kind: session.Failed, Err: session.ErrTransport}, {Kind: session.Progress}} {
			if err := d.step(e); err != nil {
				return err
			}
			if err := d.state([]string{"Busy — waiting for the other session", "Reconnecting", "Ready"}[i], true, []string{"busy", "transport-retry", "recovered"}[i]); err != nil {
				return err
			}
		}
		if err := d.step(session.Event{Kind: session.Failed, Err: diagnostic.NewTerminal(session.Cloud, "unauthorized", session.ErrUncertain)}); err != nil {
			return err
		}
		// Credential rejection cancels and joins through runObserved, then exposes
		// the terminal message. It is not a permanent fake controller override.
		select {
		case <-d.runner.releasing:
		case <-d.ctx.Done():
			return d.ctx.Err()
		}
		select {
		case <-d.runner.released:
		case <-d.ctx.Done():
			return d.ctx.Err()
		}
		if err := d.state("Sign-in required — correct credentials and Start again", false, "credential-rejected"); err != nil {
			return err
		}
		return d.click("quit")
	case "activate":
		d.w.Hide()
		if err := d.mark("trigger"); err != nil {
			return err
		}
		if err := guitest.Wait(d.ctx, "owner shown and focused", func() bool { return d.w.IsVisible() && d.w.IsFocused() }); err != nil {
			return err
		}
		if err := d.state("Ready", true, "activated"); err != nil {
			return err
		}
	case "strand":
		origin := strings.Split(d.url, "/#")[0]
		// about:blank has a different URL origin and requires no network or server.
		// Use page navigation, not the native SetURL whose recovery we are testing.
		d.w.ExecJS(`location.href='about:blank'`)
		if err := d.probe(`location.href === 'about:blank' && location.origin !== ` + strconv.Quote(origin) + ` && !document.getElementById('status')`); err != nil {
			return err
		}
		if err := d.serverState("Ready", true); err != nil {
			return err
		}
		if err := d.runnerUninterrupted(); err != nil {
			return err
		}
		if err := d.capture(d.ctx, filepath.Join(d.dir, "stranded.png")); err != nil {
			return err
		}
		if err := d.mark("off-origin"); err != nil {
			return err
		}
		if err := d.mark("trigger"); err != nil {
			return err
		}
		if err := d.probe(`location.origin === ` + strconv.Quote(origin) + ` && document.getElementById('status')?.textContent === 'Ready'`); err != nil {
			return err
		}
		// Ready in the reloaded DOM requires app.js's authenticated status fetch;
		// also independently check that the same authenticated server stayed active.
		if err := d.state("Ready", true, "restored"); err != nil {
			return err
		}
		if err := d.runnerUninterrupted(); err != nil {
			return err
		}
		if err := d.mark("restored"); err != nil {
			return err
		}

	case "close-ready", "close-connecting":
		if runtime.GOOS != "windows" {
			d.w.Close()
			return d.release()
		}
		fallthrough
	case "term", "interrupt", "apple":
		if err := d.mark("trigger"); err != nil {
			return err
		}
		return d.release()
	case "quit-ready", "quit-connecting":
	default:
		return fmt.Errorf("unknown GUI scenario %q", d.scenario)
	}
	if err := d.click("quit"); err != nil {
		return err
	}
	return d.release()
}

func (d *guiTestDriver) capture(ctx context.Context, path string) error {
	if runtime.GOOS == "windows" {
		d.w.Show()
		d.w.Focus()
	}
	return guitest.Capture(ctx, path, uintptr(d.w.NativeWindow()))
}

// The driver can authenticate independently while the window is on a foreign page.
func (d *guiTestDriver) serverState(want string, active bool) error {
	parts := strings.Split(d.url, "/#")
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	if err := guitest.Wait(d.ctx, "server status "+want, func() bool {
		req, _ := http.NewRequestWithContext(d.ctx, "GET", parts[0]+"/api/status", nil)
		req.Header.Set("X-Seestar-Token", parts[1])
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var s struct {
			Status string `json:"status"`
			Active bool   `json:"active"`
		}
		return resp.StatusCode == 200 && json.UnmarshalRead(io.LimitReader(resp.Body, maxJSON), &s) == nil && s.Status == want && s.Active == active
	}); err != nil {
		return err
	}
	return nil
}

func (d *guiTestDriver) runnerUninterrupted() error {
	select {
	case <-d.runner.releasing:
		return errors.New("runner stopped while window was stranded")
	case <-d.runner.entered:
		return errors.New("runner restarted during window recovery")
	default:
		return nil
	}
}
