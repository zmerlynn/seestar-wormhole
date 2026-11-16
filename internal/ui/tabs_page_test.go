package ui

import (
	"encoding/json/v2"
	"strconv"
	"testing"
)

// framedPage makes the page a frame of the shell, as each tab's page is.
const framedPage = "window.top = {};"

// A copy-only tab says so in its header and notice, naming the tab or
// window that serves the Seestar app when it knows it, and hides what
// would point the app or an SMB app at its ports.
func TestCopyOnlyPage(t *testing.T) {
	for _, holder := range []string{"tab", "window", ""} {
		for _, framed := range []bool{true, false} {
			t.Run(holder+"/"+strconv.FormatBool(framed), func(t *testing.T) {
				copyOnly := map[string]any{}
				if holder != "" {
					copyOnly = map[string]any{"profile": "Garden", "telescope": "Seestar S50 Pro TEST0002", "tab": holder == "tab"}
				}
				response := map[string]any{"active": true, "ready": true, "status": "Ready", "selected_device": "Seestar S50 Pro TEST0001", "discovery_started": true,
					"settings": testSettings(), "version": "synthetic", "copy_only": copyOnly, "serving_ports": []int{},
					"effective": map[string]any{"listen": "all_networks", "idle": map[string]string{"mode": "keep_connected"}, "ports": map[string]int{"445": 4445, "4700": 4700}},
					"password":  map[string]bool{"memory": true}, "events": []any{}}
				encoded, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				var prelude []string
				if framed {
					prelude = append(prelude, framedPage)
				}
				runPageRegression(t, "copy-only-test.js", "checkCopyOnly("+string(encoded)+","+strconv.Quote(holder)+","+strconv.FormatBool(framed)+")", prelude...)
			})
		}
	}
}

// A tab's Quit quits the whole app through the shell.
func TestFramedQuitPage(t *testing.T) {
	runPageRegression(t, "copy-only-test.js", "checkFramedQuit(true)", framedPage)
	runPageRegression(t, "copy-only-test.js", "checkFramedQuit(false)")
}
