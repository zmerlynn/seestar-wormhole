package ui

import (
	"strings"
	"testing"
)

func TestSetupHierarchyAndRunningCopy(t *testing.T) {
	b, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	// Pin the small setup fixture directly; the native driver also checks its
	// rendered control styling and Account alignment in both themes.
	last := -1
	for _, id := range []string{"start", "goal", "trust", "account", "email", "device-picker", "custom-device", "advanced"} {
		position := strings.Index(string(b), `id="`+id+`"`)
		if position < 0 || position <= last {
			t.Fatalf("setup order misplaced %s", id)
		}
		last = position
	}
	for _, obsolete := range []string{"Email and a password source are required.", "Settings are locked until the connection stops.", "Stop to change settings."} {
		if strings.Contains(string(b), obsolete) {
			t.Fatal("redundant setup/running copy retained", obsolete)
		}
	}
	if strings.Count(string(b), "Keep this app open while you use your Seestar.") != 1 {
		t.Fatal("running instructions repeated or missing")
	}
	if !strings.Contains(string(b), "seestar‑wormhole") {
		t.Fatal("product name can break at its hyphen")
	}
}

func TestManualSerialLabel(t *testing.T) {
	b, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{`<label for="serial">Serial number</label>`, `<label for="model">Model (optional)</label>`} {
		if !strings.Contains(string(b), label) {
			t.Fatal("manual field label missing", label)
		}
	}
}

func TestManualConnectGapStyle(t *testing.T) {
	css, err := assets.ReadFile("web/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "#custom-device:not([hidden]) + #picker-action {\n  margin-top: .75rem;\n}") {
		t.Fatal("Connect needs a gap only below visible manual fields")
	}
}

func TestButtonsShareFontSize(t *testing.T) {
	css, err := assets.ReadFile("web/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(css), "footer button") {
		t.Fatal("Quit must inherit the shared button font size")
	}
	if !strings.Contains(string(css), "button {\n  font:inherit;") {
		t.Fatal("buttons must inherit the shared font")
	}
	if !strings.Contains(string(css), "#start {\n  font-weight:600;\n  min-width:7rem;\n  border:2px solid var(--focus)\n}") {
		t.Fatal("Connect/Stop must retain bold text and blue border")
	}
}

// The header and tab bar come first; Connection holds the route choice and
// the account page, and Activity holds Details and logs.
func TestTabsAndPanelsOrder(t *testing.T) {
	b, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	last := -1
	for _, id := range []string{"app-header", "connection-dot", "connection-text", "tabs", "tab-connection", "tab-copy", "tab-browse", "tab-free", "tab-activity", "tabs-note", "copy-reach",
		"panel-connection", "route-choice", "route-account", "route-lan", "account-route", "announcements", "primary-action", "settings", "running",
		"lan-route", "lan-address", "lan-test", "lan-save", "lan-result", "panel-copy", "c-main", "s-box", "copy-form", "h-details", "panel-browse", "b-frame", "panel-free",
		"panel-activity", "details", "events", "error-code", "copy", "effective-settings", "copy-settings", "log-path", "config-file", "copy-file", "quit"} {
		position := strings.Index(string(b), `id="`+id+`"`)
		if position < 0 || position <= last {
			t.Fatalf("page order misplaced %s", id)
		}
		last = position
	}
	for _, want := range []string{"> Through my Seestar account</label>", "> On this network</label>", "Copy, Browse and Free up space open once you connect through your account, or save an address on this network.", `<details id="details" open><summary>Details and logs</summary>`} {
		if strings.Count(string(b), want) != 1 {
			t.Fatal("page copy missing or repeated:", want)
		}
	}
}

func TestRadiosKeepTheirNativeSize(t *testing.T) {
	css, err := assets.ReadFile("web/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "input:not([type=checkbox]):not([type=radio]),select,textarea {") {
		t.Fatal("the route choice's radios must not take the text fields' block style")
	}
}
