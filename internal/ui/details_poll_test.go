package ui

import (
	"encoding/json/v2"
	"fmt"
	"strconv"
	"testing"
	"time"
)

func TestDetailsPollWrites(t *testing.T) {
	events := make([]map[string]string, eventLimit)
	for i := range events {
		events[i] = map[string]string{"time": time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC).Format(time.RFC3339), "message": fmt.Sprintf("event-%02d", i), "code": "lifecycle"}
	}
	response := map[string]any{"active": true, "status": "Connected to your Seestar", "settings": testSettings(), "version": "synthetic", "effective": map[string]any{"listen": "this_computer", "idle": map[string]string{"mode": "keep_connected"}, "ports": map[string]int{"445": 1234}}, "password": map[string]bool{"memory": true}, "events": events, "log_path": "synthetic-path"}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"rolling", "replacement", "same length", "reorder", "effective", "path"} {
		t.Run(scenario, func(t *testing.T) {
			runPageRegression(t, "details-poll-test.js", "checkDetailsPoll("+string(encoded)+","+strconv.Quote(scenario)+")")
		})
	}
}
