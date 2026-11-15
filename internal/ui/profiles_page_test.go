package ui

import (
	"encoding/json/v2"
	"testing"
)

func TestProfilesDialogPage(t *testing.T) {
	runPageRegression(t, "profiles-test.js", "checkProfilesDialog()")
}

func TestProfileLabelPage(t *testing.T) {
	runPageRegression(t, "profiles-test.js", "checkProfileLabel()")
}

// A copy-only window says so in its header and notice, naming the window
// that serves the Seestar app when it knows it, and hides what would
// point the app or an SMB app at its ports.
func TestCopyOnlyPage(t *testing.T) {
	for _, holder := range []bool{true, false} {
		copyOnly := map[string]string{}
		if holder {
			copyOnly = map[string]string{"profile": "Garden", "telescope": "Seestar S50 Pro TEST0002"}
		}
		response := map[string]any{"active": true, "ready": true, "status": "Ready", "selected_device": "Seestar S50 Pro TEST0001", "discovery_started": true,
			"settings": testSettings(), "version": "synthetic", "copy_only": copyOnly, "serving_ports": []int{},
			"effective": map[string]any{"listen": "all_networks", "idle": map[string]string{"mode": "keep_connected"}, "ports": map[string]int{"445": 4445, "4700": 4700}},
			"password":  map[string]bool{"memory": true}, "events": []any{}}
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		holderArg := "false"
		if holder {
			holderArg = "true"
		}
		runPageRegression(t, "profiles-test.js", "checkCopyOnly("+string(encoded)+","+holderArg+")")
	}
}
