package cloudhttp

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// A repeated page can satisfy total while hiding another real pair. These
// synthetic wire literals stop exactly at total; there is no terminal fixture.
func TestCatalogDuplicateRequiresModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pages  []string
		serial string
	}{
		{"repeated page", []string{
			`{"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"first"}]}}`,
			`{"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"first"}]}}`,
		}, "private-a"},
		{"within page", []string{
			`{"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"first"},{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"later"}]}}`,
		}, "private-a"},
		{"unrelated duplicate", []string{
			`{"data":{"total":3,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"first"},{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"later"},{"deviceSn":"private-b","deviceModel":"S30","deviceStatus":1,"ssid":"other"}]}}`,
		}, "private-b"},
	} {
		for _, model := range []string{"", "S50 Pro", "S30"} {
			t.Run(tc.name+"/"+model, func(t *testing.T) {
				var logs bytes.Buffer
				f := &selectionCloud{bodies: append([]string(nil), tc.pages...)}
				c := selectionClient(t, f, &logs, slog.LevelDebug)
				got, err := c.SelectDevice(t.Context(), Device{Serial: tc.serial, Model: model})
				if model == "" {
					assertCatalogModelRequired(t, got, err)
					if strings.Contains(logs.String(), "device_selected") {
						t.Fatal("selected without model after duplicate")
					}
				} else {
					want := Device{"private-a", "S50 Pro"}
					if tc.serial == "private-b" {
						want = Device{"private-b", "S30"}
					}
					if model != want.Model {
						if err == nil || !strings.Contains(err.Error(), "--model") || got != (Device{}) {
							t.Fatalf("mismatched model selection=%v error=%v", got, err)
						}
					} else if err != nil || got != want {
						t.Fatalf("selection=%v error=%v, want received pair", got, err)
					}
				}
				if !strings.Contains(logs.String(), `"duplicate_items":1`) {
					t.Fatal("duplicate not logged")
				}
				assertCatalogPageRequests(t, f, len(tc.pages))
				assertCatalogPagePrivacy(t, logs.String())
			})
		}
	}
}

func TestCatalogOverstatedTotalWithEmptyPages(t *testing.T) {
	for _, model := range []string{"", "S50 Pro"} {
		var logs bytes.Buffer
		pages := []string{`{"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"first"}]}}`}
		for i := 1; i < 16; i++ {
			pages = append(pages, `{"data":{"total":2,"items":null}}`)
		}
		f := &selectionCloud{bodies: pages}
		c := selectionClient(t, f, &logs, slog.LevelDebug)
		got, err := c.SelectDevice(t.Context(), Device{Serial: "private-a", Model: model})
		if model == "" {
			assertCatalogModelRequired(t, got, err)
		} else if err != nil || got != (Device{"private-a", "S50 Pro"}) {
			t.Fatalf("selection=%v error=%v, want received pair", got, err)
		}
		assertCatalogPageRequests(t, f, 16)
		assertCatalogPagePrivacy(t, logs.String())
	}
}

func TestCatalogCurrentBackwardAndNegativeFallback(t *testing.T) {
	for _, tc := range []struct{ name, second, next string }{
		{"backward", `{"data":{"current":1,"total":3,"items":null}}`, "current=2&isContainShared=3&pageSize=100"},
		{"negative fallback", `{"data":{"current":-7,"total":3,"items":null}}`, "current=9&isContainShared=3&pageSize=100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			f := &selectionCloud{bodies: []string{
				`{"data":{"current":7,"total":3,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"first"}]}}`,
				tc.second,
				`{"data":{"total":3,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"first"},{"deviceSn":"private-a","deviceModel":"S30","deviceStatus":1,"ssid":"other"}]}}`,
			}}
			c := selectionClient(t, f, &logs, slog.LevelDebug)
			got, err := c.SelectDevice(t.Context(), Device{Serial: "private-a"})
			assertCatalogModelRequired(t, got, err)
			if len(f.queries) != 5 || f.queries[2] != "current=1&isContainShared=3&pageSize=100" || f.queries[3] != "current=8&isContainShared=3&pageSize=100" || f.queries[4] != tc.next || len(f.bodies) != 0 {
				t.Fatalf("queries=%v, want literal current sequence ending in %s", f.queries, tc.next)
			}
			assertCatalogPagePrivacy(t, logs.String())
		})
	}
}
