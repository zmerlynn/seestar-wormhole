package cloudhttp

import (
	"bytes"
	"encoding/json/v2"
	"log/slog"
	"strings"
	"testing"
)

func TestDeviceCatalogEveryFetch(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		count      int
		complete   bool
		message    string
	}{
		{"single", `{"data":{"total":1,"items":[{"deviceSn":"synthetic-one","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"}]}}`, 1, true, "Account telescopes: S30 synthetic-one"},
		{"multiple", twoDevices, 2, true, "Account telescopes: S50 Pro serial-sentinel-secret-1234, S30 other-serial-sentinel-5678"},
		{"incomplete", `{"data":{"items":[{"deviceSn":"synthetic-one","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"}]}}`, 1, false, "Account telescopes (list may be incomplete): S30 synthetic-one"},
		{"duplicate", `{"data":{"total":2,"items":[{"deviceSn":"synthetic-one","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"},{"deviceSn":"synthetic-one","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"}]}}`, 1, false, "Account telescopes (list may be incomplete): S30 synthetic-one"},
		{"empty", `{"data":{"total":0,"items":[]}}`, 0, false, "Account telescopes: none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			c := selectionClient(t, &selectionCloud{bodies: []string{tc.body, tc.body}}, &logs, slog.LevelInfo)
			var observed []Catalog
			c.SetCatalogObserver(func(c Catalog) { observed = append(observed, c) })
			for range 2 {
				c.SelectDevice(t.Context(), Device{})
			}
			if len(observed) != 2 {
				t.Fatalf("fetch observations=%d", len(observed))
			}
			for _, c := range observed {
				if c.Count != tc.count || c.Complete != tc.complete || c.Message() != tc.message {
					t.Fatalf("catalog=%+v message=%q", c, c.Message())
				}
			}
			records := 0
			for _, line := range strings.Split(logs.String(), "\n") {
				var record struct {
					Msg, Level string
					Devices    []CatalogPair
					Count      int
					Complete   bool
					Omitted    int
				}
				if json.Unmarshal([]byte(line), &record, json.MatchCaseInsensitiveNames(true)) != nil || record.Msg != "device_catalog" {
					continue
				}
				records++
				if record.Level != "INFO" || record.Count != tc.count || record.Complete != tc.complete || record.Omitted != 0 || len(record.Devices) != tc.count {
					t.Fatalf("record=%+v", record)
				}
				for i, p := range record.Devices {
					if p != observed[0].Pairs[i] {
						t.Fatal("logged pair differs")
					}
				}
			}
			if records != 2 {
				t.Fatalf("catalog records=%d", records)
			}
		})
	}
}

func TestCatalogEscapingAndBounds(t *testing.T) {
	c := newCatalog([]Device{{Model: "", Serial: "synthetic-one"}, {Model: "future\t\u202e", Serial: "synthetic\"\\\u202e"}}, true)
	if c.Message() != `Account telescopes: Seestar synthetic-one, future\t\u202e synthetic\"\\\u202e` {
		t.Fatalf("escaped catalog=%q", c.Message())
	}
	devices := make([]Device, 256)
	for i := range devices {
		devices[i] = Device{Model: strings.Repeat("\u202e", 64), Serial: strings.Repeat("\"", 64)}
	}
	c = newCatalog(devices, true)
	if len(c.Message()) > 4096 || !strings.HasSuffix(c.Message(), "… and 236 more") || c.Omitted == 0 || c.Count != 256 || len(c.Pairs)+c.Omitted != 256 || c.Omitted != 222 {
		t.Fatalf("unbounded catalog or missing omitted count: %q %+v", c.Message(), c)
	}
	for _, jsonFormat := range []bool{false, true} {
		var output bytes.Buffer
		var h slog.Handler = slog.NewTextHandler(&output, nil)
		if jsonFormat {
			h = slog.NewJSONHandler(&output, nil)
		}
		slog.New(h).Info("device_catalog", "devices", c.Pairs, "count", c.Count, "complete", c.Complete, "omitted", c.Omitted)
		if output.Len() > 16<<10 {
			t.Fatalf("catalog log exceeds sink bound: %d", output.Len())
		}
	}
}

func TestCatalogSerialPrivacyExceptionIsExact(t *testing.T) {
	const serial = "private-secret"
	for _, text := range []string{
		`{"msg":"device_selected","serial":"private-secret"}`,
		`{"msg":"device_catalog","devices":[{"model":"S30","serial":"private-secret"}]}`,
	} {
		if strings.Contains(withoutIdentitySerialLogs(t, text), serial) {
			t.Fatal("documented serial field not allowed")
		}
	}
	for _, text := range []string{
		`{"msg":"other","serial":"private-secret"}`,
		`{"msg":"device_catalog","serial":"private-secret"}`,
		`{"msg":"device_catalog","devices":[{"model":"private-secret","serial":"allowed"}]}`,
		`{"msg":"device_catalog","devices":[{"serial":"allowed","extra":"private-secret"}]}`,
		`{"msg":"device_selected","extra":"private-secret"}`,
		`{"msg":"device_catalog","extra":"private-secret"}`,
		`private-secret`,
	} {
		if !strings.Contains(withoutIdentitySerialLogs(t, text), serial) {
			t.Fatalf("privacy exception widened: %s", text)
		}
	}
}

func TestCatalogPartialFetch(t *testing.T) {
	var logs bytes.Buffer
	c := selectionClient(t, &selectionCloud{bodies: []string{`{"data":{"total":2,"items":[{"deviceSn":"synthetic-one","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"}]}}`, `{`}}, &logs, slog.LevelInfo)
	var got Catalog
	c.SetCatalogObserver(func(c Catalog) { got = c })
	if _, _, _, err := c.devices(t.Context()); err == nil {
		t.Fatal("partial fetch did not fail")
	}
	if got.Complete || got.Count != 1 || got.Message() != "Account telescopes (list may be incomplete): S30 synthetic-one" {
		t.Fatalf("partial catalog=%+v", got)
	}
	if strings.Count(logs.String(), `"msg":"device_catalog"`) != 1 || !strings.Contains(logs.String(), `"serial":"synthetic-one"`) {
		t.Fatal("partial catalog record missing")
	}
}
