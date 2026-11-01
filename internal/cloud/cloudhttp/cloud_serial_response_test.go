package cloudhttp

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func cloudSerialCases() []struct {
	name, serial string
	valid        bool
} {
	return []struct {
		name, serial string
		valid        bool
	}{
		{"empty", "", false}, {"one", "!", true},
		{"64", "!" + strings.Repeat("s", 62) + "~", true},
		{"65", strings.Repeat("s", 65), false},
		{"space", "s ", false}, {"DEL", "s\x7f", false},
		{"0x80", "s\u0080", false}, {"NUL", "s\x00", false},
		{"control", "s\x01", false},
		{"escape expansion", strings.Repeat("\x01", 4096), false},
	}
}

func cloudSerialBody(t *testing.T, serial string, list bool) string {
	t.Helper()
	d := map[string]any{"deviceSn": serial, "deviceModel": "S30", "deviceStatus": 1, "ssid": "synthetic"}
	var data any = d
	if list {
		data = map[string]any{"total": 1, "items": []any{d}}
	}
	b, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCloudSerialCatalog(t *testing.T) {
	for _, tc := range cloudSerialCases() {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			c := selectionClient(t, &selectionCloud{bodies: []string{cloudSerialBody(t, tc.serial, true)}}, &logs, slog.LevelInfo)
			var catalog Catalog
			c.catalogObserver = func(v Catalog) { catalog = v }
			d, err := c.SelectDevice(t.Context(), Device{})
			if tc.valid {
				if err != nil || d.Serial != tc.serial || d.DisplayName() != "S30 "+tc.serial || len(catalog.Pairs) != 1 || catalog.Pairs[0].Serial != tc.serial {
					t.Fatalf("valid cloud identity changed: device=%v catalog=%v err=%v", d, catalog, err)
				}
				if !strings.Contains(logs.String(), `"msg":"device_selected"`) {
					t.Fatal("valid selection was not published")
				}
			} else {
				if !errors.Is(err, cloud.ErrInvalid) || errors.Is(err, cloud.ErrConfig) || d != (Device{}) {
					t.Fatalf("invalid cloud identity selected: bytes=%d device=%v err=%v", len(tc.serial), d, err)
				}
				if len(catalog.Pairs) != 0 || strings.Contains(logs.String(), `"msg":"device_selected"`) {
					t.Fatal("invalid cloud identity published")
				}
			}
		})
	}
}

func TestInvalidDetailResponse(t *testing.T) {
	for _, tc := range cloudSerialCases() {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			c := selectionClient(t, &selectionCloud{bodies: []string{emptyDevices}, detailBody: cloudSerialBody(t, tc.serial, false)}, &logs, slog.LevelInfo)
			_, err := c.SelectDevice(t.Context(), Device{Serial: "synthetic-requested", Model: "S30"})
			if tc.valid {
				// A valid successful mismatch still gives selection guidance.
				if !errors.Is(err, cloud.ErrConfig) || errors.Is(err, cloud.ErrInvalid) {
					t.Fatalf("valid mismatch lost guidance: %v", err)
				}
			} else if !errors.Is(err, cloud.ErrInvalid) || errors.Is(err, cloud.ErrConfig) {
				t.Fatalf("invalid detail response reclassified: %v", err)
			}
			if strings.Contains(logs.String(), `"msg":"device_selected"`) {
				t.Fatal("mismatching detail published as selected")
			}
		})
	}
}

func TestCloudSerialDetailCorrelation(t *testing.T) {
	for _, tc := range cloudSerialCases() {
		if tc.valid {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			body := cloudSerialBody(t, tc.serial, false)
			for _, response := range []struct {
				name, body string
				want       error
			}{
				{"success", body, cloud.ErrInvalid},
				{"malformed status", strings.Replace(body, `"deviceStatus":1`, `"deviceStatus":null`, 1), cloud.ErrInvalid},
				{"refusal", `{"code":300403,` + body[1:], cloud.ErrBusy},
			} {
				t.Run(response.name, func(t *testing.T) {
					for _, parse := range []func([]byte) (cloud.Detail, error){cloud.ParseDetail, cloud.ParseLANDetail} {
						d, err := parse([]byte(response.body))
						if !errors.Is(err, response.want) || d.Serial() != "" {
							t.Fatalf("invalid identity retained for correlation: serialBytes=%d err=%v", len(d.Serial()), err)
						}
					}
				})
			}
		})
	}
}
