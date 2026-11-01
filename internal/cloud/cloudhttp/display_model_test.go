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
			if event.Model != tc.want || event.Serial != "***1234" {
				t.Fatalf("event=%+v", event)
			}
			if d.DisplayName() != tc.want+" ***1234" {
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
			if got.DisplayName() != model+" ***1234" {
				t.Fatalf("display=%q", got.DisplayName())
			}
			assertSelectionSecrets(t, logs.String()+got.DisplayName())
		})
	}
}
