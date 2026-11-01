package ui

import (
	"encoding/json/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

func TestLifecycleFullSerial(t *testing.T) {
	for _, serial := range []string{strings.Repeat("s", 65), strings.Repeat("\u202e", 11)} {
		escaped := strconv.QuoteToASCII(serial)
		escaped = escaped[1 : len(escaped)-1]
		for _, model := range []string{"S30", ""} {
			t.Run(model, func(t *testing.T) {
				c := testController(nil)
				c.log = &eventLog{storage: testStorage(t)}
				c.status = proxy.NewStatus()
				c.status.SetSelectedDevice(cloudhttp.Device{Serial: serial, Model: model})
				message := telescopeText("Ready", c.status.SelectedDevice())
				prefix := model
				if prefix == "" {
					prefix = "Seestar"
				}
				want := "Connected to " + prefix + " " + escaped
				c.recordProgress(message)
				events, _, _ := c.log.snapshot()
				if message != want || len(events) != 1 || events[0].Message != want {
					t.Errorf("selected lifecycle truncated: got %q want %q; events=%+v", message, want, events)
				}
				response := map[string]any{"active": true, "status": message, "settings": testSettings(), "version": "synthetic", "effective": map[string]any{"listen": "this_computer", "idle": map[string]string{"mode": "keep_connected"}, "ports": map[string]int{}}, "password": map[string]bool{"memory": true}, "events": events}
				encoded, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				runPageRegression(t, "connected-status-test.js", "checkConnectedStatus("+string(encoded)+","+strconv.Quote(want)+")")
				catalog := cloudhttp.Catalog{Pairs: []cloudhttp.CatalogPair{{Model: "S30", Serial: escaped}}, Count: 1, Complete: true}
				c.log.addCatalog(catalog)
				events, path, failed := c.log.snapshot()
				if failed || len(events) != 2 || events[1].Message != "Account telescopes: S30 "+escaped {
					t.Fatal("catalog event lost full serial")
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				if len(lines) != 3 {
					t.Fatalf("private records=%d", len(lines))
				}
				var lifecycle struct {
					Message string `json:"message"`
				}
				var record struct {
					Msg string `json:"msg"`
					cloudhttp.Catalog
				}
				if json.Unmarshal([]byte(lines[0]), &lifecycle) != nil || lifecycle.Message != want {
					t.Fatal("private lifecycle lost full serial")
				}
				if json.Unmarshal([]byte(lines[2]), &record) != nil || record.Msg != "device_catalog" || len(record.Pairs) != 1 || record.Pairs[0].Serial != escaped {
					t.Fatal("private catalog lost full serial")
				}
				response["events"] = events
				encoded, err = json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				runPageRegression(t, "full-serial-test.js", "checkFullSerial("+string(encoded)+","+strconv.Quote(want)+","+strconv.Quote(catalog.Message())+")")
			})
		}
	}
}
