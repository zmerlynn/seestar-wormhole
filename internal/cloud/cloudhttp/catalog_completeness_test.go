package cloudhttp

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Synthetic literals end at the last page; any extra request is a fixture error.
func TestCatalogCompleteness(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pages      []string
		serialSafe bool
	}{
		{"absent", []string{`{"data":{"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1}]}}`}, false},
		{"null", []string{`{"data":{"total":null,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1}]}}`}, false},
		{"negative", []string{`{"data":{"total":-7,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1}]}}`}, false},
		{"zero", []string{`{"data":{"total":0,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1}]}}`}, false},
		{"understated", []string{`{"data":{"total":1,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1},{"deviceSn":"private-b","deviceModel":"S30","ssid":"second","deviceStatus":1}]}}`}, false},
		{"exact", []string{`{"data":{"total":1,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1}]}}`}, true},
		{"exact duplicates", []string{`{"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1},{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"duplicate","deviceStatus":2}]}}`}, false},
		{"understated duplicates", []string{`{"data":{"total":1,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1},{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"duplicate","deviceStatus":2}]}}`}, false},
		{"understated accumulated duplicates", []string{pageA3, `{"data":{"total":1,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"duplicate","deviceStatus":2}]}}`}, false},
		{"absent later total", []string{pageA3, `{"data":{"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"duplicate","deviceStatus":2}]}}`}, false},
		{"page bound", repeatCatalogPage(pageA257, 16), false},
	} {
		for _, model := range []string{"", "S50 Pro"} {
			t.Run(tc.name+"/"+model, func(t *testing.T) {
				var logs bytes.Buffer
				f := &selectionCloud{bodies: append([]string(nil), tc.pages...)}
				c := selectionClient(t, f, &logs, slog.LevelDebug)
				got, err := c.SelectDevice(t.Context(), Device{Serial: "private-a", Model: model})
				if !tc.serialSafe && model == "" {
					assertCatalogModelRequired(t, got, err)
				} else if err != nil || got != (Device{"private-a", "S50 Pro"}) {
					t.Fatalf("selection=%v error=%v, want received pair", got, err)
				}
				assertCatalogPageRequests(t, f, len(tc.pages))
				assertCatalogPagePrivacy(t, logs.String())
			})
		}
	}
}

func TestCatalogSplitModelAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name, last string
		complete   bool
	}{
		{"exact", `{"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S30","ssid":"second","deviceStatus":4}]}}`, true},
		{"understated", `{"data":{"total":1,"items":[{"deviceSn":"private-a","deviceModel":"S30","ssid":"second","deviceStatus":4}]}}`, false},
	} {
		for _, model := range []string{"", "S50 Pro", "S30"} {
			t.Run(tc.name+"/"+model, func(t *testing.T) {
				var logs bytes.Buffer
				f := &selectionCloud{bodies: []string{pageA2, tc.last}}
				c := selectionClient(t, f, &logs, slog.LevelDebug)
				got, err := c.SelectDevice(t.Context(), Device{Serial: "private-a", Model: model})
				if model == "" {
					assertCatalogModelRequired(t, got, err)
					reason := "incomplete"
					if tc.complete {
						reason = "ambiguous"
					}
					if !strings.Contains(err.Error(), reason) {
						t.Fatalf("error=%v, want %s", err, reason)
					}
				} else if err != nil || got != (Device{"private-a", model}) {
					t.Fatalf("selection=%v error=%v, want requested pair", got, err)
				}
				assertCatalogPageRequests(t, f, 2)
				assertCatalogPagePrivacy(t, logs.String())
			})
		}
	}
}

func TestCatalogCurrentPage(t *testing.T) {
	for _, tc := range []struct{ name, first, next string }{
		{"positive", `{"data":{"current":7,"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1}]}}`, "current=8&pageSize=100"},
		{"absent", `{"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1}]}}`, "current=2&pageSize=100"},
		{"null", `{"data":{"current":null,"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1}]}}`, "current=2&pageSize=100"},
		{"zero", `{"data":{"current":0,"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1}]}}`, "current=2&pageSize=100"},
		{"negative", `{"data":{"current":-8,"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1}]}}`, "current=2&pageSize=100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			f := &selectionCloud{bodies: []string{tc.first, pageB2}}
			c := selectionClient(t, f, &logs, slog.LevelDebug)
			got, err := c.SelectDevice(t.Context(), Device{Serial: "private-b"})
			if err != nil || got != (Device{"private-b", "S30"}) {
				t.Fatalf("selection=%v error=%v", got, err)
			}
			if len(f.queries) != 4 || f.queries[3] != tc.next {
				t.Fatalf("queries=%v, want two pages ending in %s", f.queries, tc.next)
			}
		})
	}
}

func TestCatalogNullItems(t *testing.T) {
	var logs bytes.Buffer
	f := &selectionCloud{bodies: []string{`{"data":{"current":1,"total":1,"items":null}}`, pageA1}}
	c := selectionClient(t, f, &logs, slog.LevelDebug)
	got, err := c.SelectDevice(t.Context(), Device{Serial: "private-a"})
	if err != nil || got != (Device{"private-a", "S50 Pro"}) {
		t.Fatalf("selection=%v error=%v, want pair after null items", got, err)
	}
	assertCatalogPageRequests(t, f, 2)
}

func assertCatalogModelRequired(t *testing.T, got Device, err error) {
	t.Helper()
	var option *diagnostic.OptionError
	if got != (Device{}) || !errors.Is(err, cloud.ErrConfig) || !errors.As(err, &option) || option.Option != "--model" || !strings.Contains(err.Error(), "name --model") {
		t.Fatalf("selection=%v error=%v, want config refusal naming --model", got, err)
	}
	assertCatalogPagePrivacy(t, err.Error())
}

func TestCatalogMissingExplicitPair(t *testing.T) {
	for _, first := range []string{pageAUnknown, pageA1} {
		var logs bytes.Buffer
		f := &selectionCloud{bodies: []string{first}}
		c := selectionClient(t, f, &logs, slog.LevelDebug)
		got, err := c.SelectDevice(t.Context(), Device{"private-a", "S30"})
		if !errors.Is(err, cloud.ErrConfig) || got != (Device{}) || !strings.Contains(err.Error(), "model_mismatch") {
			t.Fatalf("unique selection=%v error=%v, want model mismatch", got, err)
		}
		assertCatalogPageRequests(t, f, 1)
	}
}

func TestCatalogCurrentPageFallbackAndRepeat(t *testing.T) {
	for _, second := range []string{
		`{"data":{"total":3,"items":[{"deviceSn":"private-b","deviceModel":"S30","ssid":"second","deviceStatus":1}]}}`,
		`{"data":{"current":null,"total":3,"items":[{"deviceSn":"private-b","deviceModel":"S30","ssid":"second","deviceStatus":1}]}}`,
		`{"data":{"current":0,"total":3,"items":[{"deviceSn":"private-b","deviceModel":"S30","ssid":"second","deviceStatus":1}]}}`,
		`{"data":{"current":7,"total":3,"items":[{"deviceSn":"private-b","deviceModel":"S30","ssid":"second","deviceStatus":1}]}}`,
	} {
		var logs bytes.Buffer
		f := &selectionCloud{bodies: []string{
			`{"data":{"current":7,"total":3,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1}]}}`,
			second,
			`{"data":{"total":3,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"duplicate","deviceStatus":2}]}}`,
		}}
		c := selectionClient(t, f, &logs, slog.LevelDebug)
		got, err := c.SelectDevice(t.Context(), Device{Serial: "private-b", Model: "S30"})
		if err != nil || got != (Device{"private-b", "S30"}) {
			t.Fatalf("selection=%v error=%v", got, err)
		}
		next := "current=9&pageSize=100"
		if strings.Contains(second, `"current":7`) {
			next = "current=8&pageSize=100"
		}
		if len(f.queries) != 5 || f.queries[3] != "current=8&pageSize=100" || f.queries[4] != next {
			t.Fatalf("queries=%v, want reply current or requested-page fallback", f.queries)
		}
	}
}

func TestCatalogCurrentPageOverflow(t *testing.T) {
	for _, model := range []string{"", "S50 Pro"} {
		var logs bytes.Buffer
		f := &selectionCloud{bodies: []string{`{"data":{"current":9223372036854775807,"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1}]}}`}}
		c := selectionClient(t, f, &logs, slog.LevelDebug)
		got, err := c.SelectDevice(t.Context(), Device{Serial: "private-a", Model: model})
		if model == "" {
			assertCatalogModelRequired(t, got, err)
		} else if err != nil || got != (Device{"private-a", "S50 Pro"}) {
			t.Fatalf("selection=%v error=%v, want received pair", got, err)
		}
		assertCatalogPageRequests(t, f, 1)
	}
}
