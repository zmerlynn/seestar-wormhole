//go:build gui && guitest

package ui

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Runs only JavaScript, without a window, browser, HTTP server, or keychain.
func TestGUITestClickAcrossStatusPoll(t *testing.T) {
	script := `
const window = {};
let clicks = 0;
const button = {
  disabled: false,
  matches() { return this.disabled; },
  scrollIntoView() {},
  focus() {},
  click() { if (!this.disabled) { clicks++; this.disabled = true; } }
};
const el = id => button;
const check = (ok, detail) => ok ? true : detail;
const assert = (ok, message) => { if (!ok) throw new Error(message); };
const first = () => ` + guiTestClickExpression("forget", 1) + `;
// A status poll can disable Remove during the probe's two paint frames.
// The click must already have happened when its probe reports success.
assert(first() === true, 'enabled click was not acknowledged');
button.disabled = true;
assert(clicks === 1, 'probe acknowledged readiness without dispatching the click');
assert(first() === true && clicks === 1, 'paint retry lost the acknowledgment');
button.disabled = false;
assert(first() === true && clicks === 1, 'paint retry clicked twice');
const second = () => ` + guiTestClickExpression("forget", 2) + `;
button.disabled = true;
assert(second() !== true && clicks === 1, 'disabled click was acknowledged');
button.disabled = false;
assert(second() === true && clicks === 2, 'enabled retry was not dispatched');
assert(second() === true && clicks === 2, 'retry clicked twice');
button.disabled = false;
assert(first() === true && clicks === 2, 'late earlier probe clicked again');
`
	runGUIJavaScript(t, script)
}

// Exercise the driver's actual receipt wrapper, not just the click predicate.
// An idle Quit can stop painting before the two-frame acknowledgment runs.
func TestGUITestClickReceiptWithoutPaint(t *testing.T) {
	for _, bridge := range []string{
		"window.chrome = {webview: {postMessage: send}};",
		"window.webkit = {messageHandlers: {external: {postMessage: send}}};",
		"window.wails = {invoke: send};",
	} {
		t.Run(bridge, func(t *testing.T) {
			script := `
const window = {};
const messages = [], frames = [];
const send = message => {
  if (message === 'wails:event:emit:guitest:probe:1') assert(clicks >= 1, 'receipt preceded click dispatch');
  if (message === 'wails:event:emit:guitest:probe:3') assert(clicks === 2, 'new receipt preceded click dispatch');
  messages.push(message);
};
` + bridge + `
const requestAnimationFrame = callback => frames.push(callback);
let clicks = 0, inheritedDisabled = false, disableOnFocus = false;
let button = {
  disabled: false,
  matches() { return inheritedDisabled; },
  scrollIntoView() {},
  focus() { if (disableOnFocus) this.disabled = true; },
  click() { clicks++; this.disabled = true; document.hidden = true; }
};
const document = {
  hidden: false, visibilityState: 'visible', hasFocus: () => true,
  getElementById: id => button
};
const assert = (ok, message) => { if (!ok) throw new Error(message); };
const count = sequence => messages.filter(m => m === 'wails:event:emit:guitest:probe:' + sequence).length;
const click = () => ` + guiTestClickProbeScript("quit", 1) + `;
button.disabled = true;
click();
assert(clicks === 0 && count(1) === 0, 'disabled click received success receipt');
button.disabled = false;
inheritedDisabled = true;
click();
assert(clicks === 0 && count(1) === 0, 'fieldset-disabled click received success receipt');
inheritedDisabled = false;
disableOnFocus = true;
click();
assert(clicks === 0 && count(1) === 0, 'focus-disabled click received success receipt');
disableOnFocus = false;
button.disabled = false;
click();
assert(clicks === 1, 'enabled click was not dispatched');
assert(count(1) === 1, 'dispatch receipt was blocked on paint during Quit');
assert(frames.length === 0, 'dispatch scheduled work on a terminating window');
assert(messages.includes('wails:event:emit:guitest:probe:1:started') &&
       messages.includes('wails:event:emit:guitest:probe:1:matched') &&
       messages.includes('wails:event:emit:guitest:probe:1:evaluated'), 'receipt protocol events missing');
click();
button.disabled = false;
click();
assert(clicks === 1 && count(1) === 3, 'retry lost receipt or dispatched twice');
// Ordinary state probes still require two paint frames and their own receipt.
messages.length = 0;
document.hidden = false;
` + guiTestProbeScript("check(true,{})", 2, true) + `;
assert(count(1) === 0 && count(2) === 0 && frames.length === 1, 'state probe skipped paint');
frames.shift()();
assert(count(2) === 0 && frames.length === 1, 'state probe skipped second paint');
frames.shift()();
assert(count(2) === 1, 'state probe lost its named receipt');
` + guiTestClickProbeScript("quit", 3) + `;
click();
assert(clicks === 2 && count(3) === 1 && count(2) === 1, 'late probe redispatched or acknowledged another sequence');
// Missing controls report diagnostics without success or an exception.
button = null;
` + guiTestClickProbeScript("quit", 4) + `;
assert(count(4) === 0 && clicks === 2, 'missing control received success receipt');
assert(!messages.some(m => m.includes('probeError')), 'probe evaluation threw');
`
			runGUIJavaScript(t, script)
		})
	}
}

// Keep the real callers, sequence allocation, subscriptions, retries, and wait.
// Only ExecJS and the native event transport are replaced. Each engine run
// replays the prior turns to preserve the modeled document without a new runner.
func TestGUITestClickDriverReceipts(t *testing.T) {
	runGUIJavaScript(t, "") // Skip before entering the driver if the engine is absent.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	listeners := map[string]func(*application.CustomEvent){}
	d := &guiTestDriver{ctx: ctx, out: io.Discard, seq: 7}
	d.on = func(name string, callback func(*application.CustomEvent)) func() {
		if listeners[name] != nil {
			t.Fatalf("duplicate subscription: %s", name)
		}
		listeners[name] = callback
		return func() { delete(listeners, name) }
	}
	history := `
const window = {}, frames = [];
let messages = [], clicks = 0, stateReady = false;
const assert = (ok, message) => { if (!ok) throw new Error(message); };
window.wails = {invoke: message => {
  if (/^wails:event:emit:guitest:probe:(8|10)$/.test(message))
    assert(clicks >= (message.endsWith(':8') ? 1 : 2), 'receipt preceded click dispatch');
  messages.push(message);
}};
const requestAnimationFrame = callback => frames.push(callback);
const button = {
  disabled: true, matches() { return this.disabled; },
  scrollIntoView() {}, focus() {}, click() { clicks++; this.disabled = true; }
};
const document = {hidden: false, getElementById: id => id === 'quit' ? button : null};
`
	var firstClick string
	for step, kind := range []string{"click", "state", "click"} {
		sequence := 8 + step
		name := fmt.Sprintf("guitest:probe:%d", sequence)
		attempts := 0
		var submitted string
		d.execJS = func(script string) {
			attempts++
			if attempts > 3 {
				t.Fatal("driver retried after receipt delivery")
			}
			if attempts == 1 {
				submitted = script
			} else if script != submitted {
				t.Fatal("retry changed the submitted sequence or script")
			}
			if len(listeners) != 4 {
				t.Fatalf("execution needs all four subscriptions, got %d", len(listeners))
			}
			for _, suffix := range []string{"", ":started", ":matched", ":evaluated"} {
				if listeners[name+suffix] == nil {
					t.Fatalf("missing subscription: %s", name+suffix)
				}
			}
			history += "\nmessages = [];\n"
			if attempts == 1 {
				history += "button.disabled = true; stateReady = false;\n"
			} else if attempts == 2 {
				history += "button.disabled = false; stateReady = true;\n"
			}
			history += script + ";\n"
			events := []string{name + ":started"}
			if attempts > 1 {
				if kind == "state" {
					history += `
assert(!messages.includes('wails:event:emit:` + name + `') && frames.length === 1, 'state caller skipped paint');
frames.shift()();
assert(!messages.includes('wails:event:emit:` + name + `') && frames.length === 1, 'state caller skipped second frame');
frames.shift()();
`
					events = append(events, name+":matched", name+":evaluated", name)
				} else {
					history += fmt.Sprintf("assert(window.__guitestClick === %d, 'click guard disagrees with receipt sequence');\n", sequence)
					events = append(events, name+":matched", name, name+":evaluated")
				}
			} else {
				events = append(events, name+":evaluated")
			}
			wire := make([]string, len(events))
			for i, event := range events {
				wire[i] = "wails:event:emit:" + event
			}
			encoded, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			wantClicks := (step + 1) / 2
			if kind == "click" && attempts > 1 {
				wantClicks++
			}
			history += `
assert(frames.length === 0, 'click receipt waited for paint or state left frames pending');
assert(!messages.some(m => m.includes('probeError')), 'driver script threw');
assert(JSON.stringify(messages.filter(m => m.startsWith('wails:event:emit:'))) === JSON.stringify(` + string(encoded) + `), 'driver receipt names, order, or dispatch mode disagree');
assert(clicks === ` + fmt.Sprint(wantClicks) + `, 'retry lost or repeated dispatch');
`
			// Validate the exact emitted event list before forwarding those events
			// to Go. Drop the first success receipt to exercise the real retry loop.
			runGUIJavaScript(t, history)
			for _, event := range events {
				if event != name || attempts == 3 {
					listeners[event](nil)
				}
			}
		}
		var err error
		if kind == "click" {
			err = d.click("quit")
		} else {
			err = d.probe("check(stateReady,{})")
		}
		if err != nil {
			t.Fatal(err)
		}
		if attempts != 3 || d.seq != sequence || len(listeners) != 0 {
			t.Fatalf("%s: attempts=%d sequence=%d listeners=%d; want 3, %d, 0", kind, attempts, d.seq, len(listeners), sequence)
		}
		if step == 0 {
			firstClick = submitted
		}
	}
	history += "\nmessages = []; button.disabled = false;\n" + firstClick + `;
assert(clicks === 2, 'late earlier driver sequence redispatched');
assert(messages.includes('wails:event:emit:guitest:probe:8'), 'late driver script changed receipt name');
assert(!messages.includes('wails:event:emit:guitest:probe:10'), 'late script acknowledged a newer sequence');
`
	runGUIJavaScript(t, history)
}

func runGUIJavaScript(t *testing.T, script string) {
	t.Helper()
	jsc, err := exec.LookPath("jsc")
	if err != nil {
		t.Skip("JavaScriptCore shell is not installed")
	}
	path := filepath.Join(t.TempDir(), "click.js")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.CommandContext(t.Context(), jsc, path).CombinedOutput(); err != nil {
		t.Fatalf("click regression: %v\n%s", err, output)
	}
}
