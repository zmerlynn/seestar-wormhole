//go:build gui && guitest

package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
