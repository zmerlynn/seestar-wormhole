package cloudhttp

import (
	"bytes"
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"log/slog"
	"strings"
	"testing"
)

func TestDisplayModel(t *testing.T) {
	for _, tc := range []struct{ name, wire, want string }{
		{"known", "Seestar S50 Pro", "Seestar S50 Pro"},
		{"unknown", "Future telescope", "Future telescope"},
		{"controls", "model\n\t\x1b\x7f", `model\n\t\x1b\x7f`},
		{"format", "model\u202e\u200d", `model\u202e\u200d`},
		{"quotes", "a\"\\b", `a\"\\b`},
		{"unicode", "星", `\u661f`},
		{"boundary", strings.Repeat("x", 64), strings.Repeat("x", 64)},
		{"exact escaped tab boundary", strings.Repeat("x", 63) + "\t", strings.Repeat("x", 61) + "..."},
		{"exact escaped quote boundary", strings.Repeat("x", 63) + "\"", strings.Repeat("x", 61) + "..."},
		{"exact escaped backslash boundary", strings.Repeat("x", 63) + "\\", strings.Repeat("x", 61) + "..."},
		{"long", strings.Repeat("x", 65), strings.Repeat("x", 61) + "..."},
		{"escaped bound", strings.Repeat("\u202e", 11), strings.Repeat(`\u202e`, 10) + "..."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayModel(tc.wire); got != tc.want {
				t.Fatalf("display=%q want=%q", got, tc.want)
			}
			var logs bytes.Buffer
			c := &Client{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			d := c.selectedDevice(detailForModel(t, tc.wire))
			if d.Model != tc.wire || d.Serial != "synthetic-model-1234" {
				t.Fatal("wire identity changed")
			}
			var event struct {
				Model  string `json:"model"`
				Serial string `json:"serial"`
			}
			if err := json.Unmarshal(logs.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			if event.Model != tc.want || event.Serial != "synthetic-model-1234" {
				t.Fatalf("event=%+v", event)
			}
			if d.DisplayName() != tc.want+" synthetic-model-1234" {
				t.Fatal("status model differs")
			}
		})
	}
}

func detailForModel(t *testing.T, model string) cloud.Detail {
	t.Helper()
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	devices, _, err := cloud.ParseDevices([]byte(`{"data":{"total":1,"items":[{"deviceSn":"synthetic-model-1234","deviceModel":` + string(encoded) + `,"deviceStatus":1,"ssid":"synthetic"}]}}`))
	if err != nil || len(devices) != 1 {
		t.Fatal("parse model", err)
	}
	return devices[0]
}

func TestCatalogPublicModelNames(t *testing.T) {
	for _, model := range []string{"Seestar S50", "Seestar S50 Pro", "Seestar S30", "Seestar S30 Pro", "Seestar S30 Plus", "Seestar S80"} {
		t.Run(model, func(t *testing.T) {
			var logs bytes.Buffer
			f := &selectionCloud{bodies: []string{strings.Replace(oneDevice, "S50 Pro", model, 1)}}
			c := selectionClient(t, f, &logs, slog.LevelInfo)
			got, err := c.SelectDevice(t.Context(), Device{})
			if err != nil || got.Model != model {
				t.Fatalf("model=%q error=%v", got.Model, err)
			}
			if !strings.Contains(logs.String(), `"model":"`+model+`"`) {
				t.Fatalf("public model missing: %s", &logs)
			}
			if got.DisplayName() != model+" serial-sentinel-secret-1234" {
				t.Fatalf("display=%q", got.DisplayName())
			}
			assertSelectionSecrets(t, logs.String()+got.DisplayName())
		})
	}
}

func TestDisplaySerial(t *testing.T) {
	for _, tc := range []struct{ wire, want string }{
		{"tiny", "tiny"},
		{"synthetic-serial-1234", "synthetic-serial-1234"},
		{"synthetic\n\t\x1b\x7f\u202e\u200d", `synthetic\n\t\x1b\x7f\u202e\u200d`},
		{"a\"\\星", `a\"\\\u661f`},
		{strings.Repeat("x", 64), strings.Repeat("x", 64)},
		{strings.Repeat("x", 63) + "\t", strings.Repeat("x", 61) + "..."},
		{strings.Repeat("\u202e", 11), strings.Repeat(`\u202e`, 10) + "..."},
	} {
		t.Run(tc.want, func(t *testing.T) {
			for _, model := range []string{"", "Seestar S50 Pro"} {
				var logs bytes.Buffer
				c := &Client{logger: slog.New(slog.NewJSONHandler(&logs, nil))}
				encoded, err := json.Marshal(map[string]any{"data": map[string]any{"items": []any{map[string]any{"deviceSn": tc.wire, "deviceModel": "Seestar S50 Pro", "deviceStatus": 1, "ssid": "synthetic"}}}})
				if err != nil {
					t.Fatal(err)
				}
				devices, _, err := cloud.ParseDevices(encoded)
				if err != nil || len(devices) != 1 {
					t.Fatal("parse serial", err)
				}
				d := c.selectedDevice(devices[0])
				d.Model = model
				if d.Serial != tc.wire {
					t.Fatal("wire serial changed")
				}
				var event struct {
					Serial string `json:"serial"`
				}
				if err := json.Unmarshal(logs.Bytes(), &event); err != nil {
					t.Fatal(err)
				}
				if event.Serial != tc.want {
					t.Fatalf("serial log=%q want=%q", event.Serial, tc.want)
				}
				label := model
				if label == "" {
					label = "Seestar"
				}
				if d.DisplayName() != label+" "+tc.want {
					t.Fatalf("label=%q", d.DisplayName())
				}
			}
		})
	}
}
