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
