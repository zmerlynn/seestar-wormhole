package cloudhttp

import (
	"bytes"
	"encoding/json/v2"
	"log/slog"
	"strconv"
	"strings"
	"testing"
)

func TestFullSerialSurfaces(t *testing.T) {
	for _, tc := range []struct{ serial, display string }{{strings.Repeat("s", 64), strings.Repeat("s", 64)}, {strings.Repeat("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", 2), strings.Repeat("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", 2)}} {
		serial := tc.serial
		t.Run(strconv.QuoteToASCII(serial), func(t *testing.T) {
			if !ValidSerial(serial) {
				t.Fatal("fixture must be accepted")
			}
			body, err := json.Marshal(map[string]any{"data": map[string]any{"total": 1, "items": []any{map[string]any{"deviceSn": serial, "deviceModel": "S30", "deviceStatus": 1, "ssid": "synthetic"}}}})
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			c := selectionClient(t, &selectionCloud{bodies: []string{string(body)}}, &logs, slog.LevelInfo)
			var catalog Catalog
			c.SetCatalogObserver(func(v Catalog) { catalog = v })
			got, err := c.SelectDevice(t.Context(), Device{})
			if err != nil || got.Serial != serial {
				t.Fatalf("selection: %v", err)
			}
			escaped := tc.display
			if got.ChoiceLabel() != "S30 "+escaped {
				t.Error("picker lost serial")
			}
			if got.DisplayName() != "S30 "+escaped {
				t.Errorf("status identity=%q want=%q", got.DisplayName(), "S30 "+escaped)
			}
			got.Model = ""
			if got.DisplayName() != "Seestar "+escaped {
				t.Errorf("empty-model identity=%q want=%q", got.DisplayName(), "Seestar "+escaped)
			}
			if len(catalog.Pairs) != 1 || catalog.Pairs[0].Serial != escaped || catalog.Message() != "Account telescopes: S30 "+escaped {
				t.Errorf("catalog=%+v message=%q want serial=%q", catalog, catalog.Message(), escaped)
			}
			selectedRecords, catalogRecords := 0, 0
			for _, line := range strings.Split(logs.String(), "\n") {
				var record struct {
					Msg, Serial string
					Devices     []CatalogPair
				}
				if json.Unmarshal([]byte(line), &record, json.MatchCaseInsensitiveNames(true)) != nil {
					continue
				}
				if record.Msg == "device_selected" {
					selectedRecords++
					if record.Serial != escaped {
						t.Errorf("selected log serial=%q want=%q", record.Serial, escaped)
					}
				}
				if record.Msg == "device_catalog" {
					catalogRecords++
					if len(record.Devices) != 1 || record.Devices[0].Serial != escaped {
						t.Errorf("catalog log=%+v want=%q", record.Devices, escaped)
					}
				}
			}
			if selectedRecords != 1 || catalogRecords != 1 {
				t.Fatalf("selected records=%d catalog records=%d", selectedRecords, catalogRecords)
			}
		})
	}
}

func TestFullSerialCatalogBudgets(t *testing.T) {
	for _, tc := range []struct {
		name, serial string
		pairs        int
		omitted      int
	}{
		{"maximum ASCII", strings.Repeat("s", 64), 2, 0},
		{"maximum escaping", strings.Repeat("\"", 64), 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !ValidSerial(tc.serial) {
				t.Fatal("maximum serial rejected")
			}
			c := newCatalog([]Device{{Model: "S30", Serial: tc.serial}, {Model: "S30", Serial: "synthetic-tail"}}, true)
			if c.Count != 2 || !c.Complete || len(c.Pairs) != tc.pairs || c.Omitted != tc.omitted {
				t.Fatalf("catalog count=%d pairs=%d omitted=%d", c.Count, len(c.Pairs), c.Omitted)
			}
			if c.Pairs[0].Serial != displaySerial(tc.serial) {
				t.Fatal("catalog cut serial")
			}
			if c.Message() != "Account telescopes: S30 "+displaySerial(tc.serial)+", S30 synthetic-tail" {
				t.Fatalf("message=%q", c.Message())
			}

			devices := make([]Device, 256)
			for i := range devices {
				devices[i] = Device{Model: "S30", Serial: tc.serial}
			}
			c = newCatalog(devices, true)
			if c.Omitted == 0 || c.Count != 256 || len(c.Pairs)+c.Omitted != 256 || len(c.Message()) > 4096 || !strings.Contains(c.Message(), " more") {
				t.Fatal("catalog budgets not enforced")
			}
			b, err := json.Marshal(c.Pairs)
			if err != nil {
				t.Fatal(err)
			}
			if len(b) > 12<<10 {
				t.Fatalf("array bytes=%d", len(b))
			}
			for _, format := range []string{"json", "text"} {
				var b bytes.Buffer
				var h slog.Handler = slog.NewJSONHandler(&b, nil)
				if format == "text" {
					h = slog.NewTextHandler(&b, nil)
				}
				slog.New(h).Info("device_catalog", "devices", c.Pairs, "count", c.Count, "complete", c.Complete, "omitted", c.Omitted)
				if b.Len() > 16<<10 {
					t.Fatalf("%s record bytes=%d", format, b.Len())
				}
				t.Logf("%s catalog bytes=%d", format, b.Len())
			}
		})
	}
}
