package ui

import (
	"encoding/json/v2"
	"testing"
)

func TestDetailsPollWrites(t *testing.T) {
	response := map[string]any{"active": true, "status": "Connected to your Seestar", "settings": testSettings(), "version": "synthetic", "effective": map[string]any{"listen": "this_computer", "idle": map[string]string{"mode": "keep_connected"}, "ports": map[string]int{"445": 1234}}, "password": map[string]bool{"memory": true}, "events": []map[string]string{{"time": "2026-01-01T00:00:00Z", "message": "App opened.", "code": "lifecycle"}}, "log_path": "synthetic-path"}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	runPageRegression(t, "details-poll-test.js", "checkDetailsPoll("+string(encoded)+")")
}
