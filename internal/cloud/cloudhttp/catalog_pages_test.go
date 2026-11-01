package cloudhttp

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"encoding/json/jsontext"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Independent response literals: none are made with the parser or a production
// encoder. Page order and expected identities are explicit semantic oracles.
const (
	pageA3       = `{"code":200,"data":{"total":3,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1}]}}`
	pageAB2      = `{"code":200,"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1},{"deviceSn":"private-b","deviceModel":"S30","ssid":"synthetic-wifi","deviceStatus":1}]}}`
	pageA2       = `{"code":200,"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1}]}}`
	pageA1       = `{"code":200,"data":{"total":1,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1}]}}`
	pageAUnknown = `{"code":200,"data":{"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1}]}}`
	pageA257     = `{"code":200,"data":{"total":257,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1}]}}`
	pageB2       = `{"code":200,"data":{"total":2,"items":[{"deviceSn":"private-b","deviceModel":"S30","ssid":"synthetic-wifi","deviceStatus":1}]}}`
	pageAB3      = `{"data":{"total":3,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1},{"deviceSn":"private-b","deviceModel":"S30","ssid":"synthetic-wifi","deviceStatus":1}]}}`
)

var catalogPageLiterals = []struct {
	name     string
	pages    []string
	want     [][2]string
	complete bool
}{
	{"exact total and overlap", []string{pageA3, pageAB3}, [][2]string{{"private-a", "S50 Pro"}, {"private-b", "S30"}}, true},
	{"changed total below accumulated", []string{pageA3, pageAB2}, [][2]string{{"private-a", "S50 Pro"}, {"private-b", "S30"}}, false},
	{"empty page without total", []string{pageA3, `{"data":{"items":[]}}`}, [][2]string{{"private-a", "S50 Pro"}}, false},
	{"large total hint", []string{pageA257, `{"data":{"total":0,"items":[]}}`}, [][2]string{{"private-a", "S50 Pro"}}, false},
	{"identical overlap", []string{pageA3, pageA3, `{"data":{"total":3,"items":[{"deviceSn":"private-b","deviceModel":"S30","ssid":"synthetic-wifi","deviceStatus":1}]}}`}, [][2]string{{"private-a", "S50 Pro"}, {"private-b", "S30"}}, true},
	{"changed total without overlap", []string{pageA3, pageB2}, [][2]string{{"private-a", "S50 Pro"}, {"private-b", "S30"}}, true},
	{"total disappears", []string{pageA2, pageAUnknown}, [][2]string{{"private-a", "S50 Pro"}}, false},
	{"total shrinks below accumulated", []string{pageA3, pageA1}, [][2]string{{"private-a", "S50 Pro"}}, false},
	{"null items", []string{pageA3, `{"data":{"total":2,"items":null}}`, pageB2}, [][2]string{{"private-a", "S50 Pro"}, {"private-b", "S30"}}, true},
	{"absent items", []string{pageA3, `{"data":{"total":2}}`, pageB2}, [][2]string{{"private-a", "S50 Pro"}, {"private-b", "S30"}}, true},
	{"empty pages with stale total bounded", repeatCatalogPage(`{"data":{"total":257,"items":[]}}`, 16), nil, false},
}

func TestCatalogPageLiterals(t *testing.T) {
	for _, tc := range catalogPageLiterals {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			f := &selectionCloud{bodies: append([]string(nil), tc.pages...)}
			c := selectionClient(t, f, &logs, slog.LevelDebug)
			got, complete, _, err := c.devices(t.Context())
			if err != nil {
				t.Fatalf("safe page sequence rejected: %v", err)
			}
			var pairs [][2]string
			for _, d := range got {
				pairs = append(pairs, [2]string{d.Serial(), d.Model()})
			}
			if complete != tc.complete {
				t.Fatalf("complete=%v, want %v", complete, tc.complete)
			}
			if !reflect.DeepEqual(pairs, tc.want) {
				t.Fatalf("identities=%v, want %v", pairs, tc.want)
			}
			assertCatalogPageRequests(t, f, len(tc.pages))
			assertCatalogPagePrivacy(t, logs.String())
		})
	}
}

func TestCatalogPageSelection(t *testing.T) {
	for _, tc := range []struct {
		name        string
		pages       []string
		input, want Device
		reason      string
	}{
		{"duplicate blocks automatic selection", []string{pageA3, pageAB3}, Device{}, Device{}, "incomplete catalog"},
		{"model cannot disambiguate", []string{pageA3, pageAB3}, Device{Model: "S50 Pro"}, Device{}, "multiple devices on account"},
		{"explicit first still reads all pages", []string{pageA3, pageAB3}, Device{Serial: "private-a", Model: "S50 Pro"}, Device{"private-a", "S50 Pro"}, ""},
		{"explicit later", []string{pageA3, pageAB3}, Device{Serial: "private-b", Model: "S30"}, Device{"private-b", "S30"}, ""},
		{"explicit absent", []string{pageA3, pageAB3}, Device{Serial: "private-missing", Model: "S30"}, Device{}, "--serial not found on account"},
		{"unique exact total", []string{pageA1}, Device{}, Device{"private-a", "S50 Pro"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			f := &selectionCloud{bodies: append([]string(nil), tc.pages...)}
			c := selectionClient(t, f, &logs, slog.LevelDebug)
			got, err := c.SelectDevice(t.Context(), tc.input)
			if got != tc.want {
				t.Fatalf("selection=%v, want %v", got, tc.want)
			}
			if tc.reason == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, cloud.ErrConfig) || !strings.Contains(err.Error(), tc.reason) {
					t.Fatalf("error=%v, want config/%s", err, tc.reason)
				}
				var option *diagnostic.OptionError
				wantOption, wantRule := "--serial", "select an existing device from the account catalog"
				if tc.reason == "incomplete catalog" {
					wantOption, wantRule = "--model", "incomplete catalog; name --model to select a received pair"
				}
				if !errors.As(err, &option) || option.Option != wantOption || option.Rule != wantRule {
					t.Fatalf("wrong option attribution: %v", err)
				}
				if strings.Contains(logs.String(), "device_selected") {
					t.Fatal("selected ambiguous catalog")
				}
				assertCatalogPagePrivacy(t, err.Error())
			}
			if tc.name == "explicit absent" {
				assertCatalogPageRequests(t, f, len(tc.pages), "/v1/my-device/detail")
			} else {
				assertCatalogPageRequests(t, f, len(tc.pages))
			}
			assertCatalogPagePrivacy(t, logs.String())
		})
	}
}

func TestCatalogPageSafety(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages []string
		want  error
	}{
		{"malformed later identity", []string{pageA3, `{"code":200,"data":{"items":[{"deviceSn":"private-b","deviceModel":3,"ssid":"synthetic-wifi","deviceStatus":1}]}}`}, cloud.ErrInvalid},
		{"malformed later total", []string{pageA3, `{"code":200,"data":{"total":"private-secret","items":[]}}`}, cloud.ErrInvalid},
		{"later refusal", []string{pageA3, `{"code":300405,"msg":"private-secret","data":{"items":[]}}`}, cloud.ErrRejected},
	} {
		for _, input := range []Device{{}, {Serial: "private-a"}} {
			t.Run(tc.name+"/"+input.Serial, func(t *testing.T) {
				var logs bytes.Buffer
				f := &selectionCloud{bodies: append([]string(nil), tc.pages...)}
				c := selectionClient(t, f, &logs, slog.LevelDebug)
				got, err := c.SelectDevice(t.Context(), input)
				if !errors.Is(err, tc.want) || got != (Device{}) {
					t.Fatalf("selection=%v error=%v, want empty/%v", got, err, tc.want)
				}
				if strings.Contains(logs.String(), "device_selected") {
					t.Fatal("selected unsafe catalog")
				}
				if errors.Is(err, cloud.ErrConfig) {
					t.Fatal("catalog safety failure attributed to --serial")
				}
				assertCatalogPageRequests(t, f, len(tc.pages))
				assertCatalogPagePrivacy(t, logs.String()+err.Error())
			})
		}
	}
}

// Generated boundary inputs use encoding/json, with literal expected counts.
// Each page is individually valid, so the 257-entry case tests aggregation.
func catalogCapacityPage(t *testing.T, start, end, total int) string {
	t.Helper()
	var items []map[string]any
	for i := start; i < end; i++ {
		items = append(items, map[string]any{"deviceSn": fmt.Sprintf("private-%03d", i), "deviceModel": "S30", "ssid": "synthetic-wifi", "deviceStatus": 1})
	}
	b, err := json.Marshal(map[string]any{"code": 200, "data": map[string]any{"total": total, "items": items}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCatalogPageCapacity(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pages   []string
		count   int
		invalid bool
	}{
		{"256 unique including overlap", []string{catalogCapacityPage(t, 0, 100, 258), catalogCapacityPage(t, 99, 199, 258), catalogCapacityPage(t, 198, 256, 258)}, 256, false},
		{"duplicates at capacity", []string{catalogCapacityPage(t, 0, 100, 356), catalogCapacityPage(t, 100, 200, 356), catalogCapacityPage(t, 200, 256, 356), catalogCapacityPage(t, 0, 100, 356)}, 256, false},
		{"257 unique", []string{catalogCapacityPage(t, 0, 100, 257), catalogCapacityPage(t, 100, 200, 257), catalogCapacityPage(t, 200, 257, 257)}, 0, true},
		{"page 16 remains unproven", repeatCatalogPage(pageA257, 16), 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			f := &selectionCloud{bodies: append([]string(nil), tc.pages...)}
			c := selectionClient(t, f, &logs, slog.LevelDebug)
			got, _, _, err := c.devices(t.Context())
			if tc.invalid {
				if !errors.Is(err, cloud.ErrInvalid) || got != nil {
					t.Fatalf("count=%d error=%v, want no partial result/invalid", len(got), err)
				}
			} else if err != nil || len(got) != tc.count {
				t.Fatalf("count=%d error=%v, want %d", len(got), err, tc.count)
			}
			assertCatalogPageRequests(t, f, len(tc.pages))
		})
	}
}

func repeatCatalogPage(page string, n int) []string {
	pages := make([]string, n)
	for i := range pages {
		pages[i] = page
	}
	return pages
}

func assertCatalogPageRequests(t *testing.T, f *selectionCloud, pages int, following ...string) {
	t.Helper()
	wantPaths := []string{"/v1/user/login", "/v1/user/current-profile"}
	for page := 1; page <= pages; page++ {
		wantPaths = append(wantPaths, "/v1/my-device")
		if len(f.queries) <= page+1 || f.queries[page+1] != fmt.Sprintf("current=%d&isContainShared=3&pageSize=100", page) {
			t.Fatalf("queries=%v, want sequential current and pageSize=100", f.queries)
		}
	}
	wantPaths = append(wantPaths, following...)
	if !reflect.DeepEqual(f.paths, wantPaths) || len(f.bodies) != 0 {
		t.Fatalf("paths=%v remaining=%d, want %v and no remaining pages (no allocation)", f.paths, len(f.bodies), wantPaths)
	}
}

func assertCatalogPagePrivacy(t *testing.T, s string) {
	t.Helper()
	assertSelectionSecrets(t, s)
	for _, private := range []string{"private-a", "private-b", "private-missing", "private-secret"} {
		if strings.Contains(s, private) {
			t.Fatal("catalog identity or input escaped diagnostics")
		}
	}
}

func TestCatalogPageDiagnostics(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		var logs bytes.Buffer
		f := &selectionCloud{bodies: []string{pageA257, pageAB2}}
		c := selectionClient(t, f, &logs, level)
		if _, _, _, err := c.devices(t.Context()); err != nil {
			t.Fatal(err)
		}
		assertCatalogPagePrivacy(t, logs.String())
		var got []map[string]any
		dec := jsontext.NewDecoder(&logs)
		for {
			var record map[string]any
			if err := json.UnmarshalDecode(dec, &record, json.MatchCaseInsensitiveNames(true)); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if record["msg"] == "cloud_catalog_pagination_oddities" {
				delete(record, "time")
				got = append(got, record)
			}
		}
		var want []map[string]any
		if level == slog.LevelDebug {
			want = []map[string]any{
				{"level": "DEBUG", "msg": "cloud_catalog_pagination_oddities", "large_total_hint": true, "changed_total_hint": false, "duplicate_items": float64(0)},
				{"level": "DEBUG", "msg": "cloud_catalog_pagination_oddities", "large_total_hint": false, "changed_total_hint": true, "duplicate_items": float64(1)},
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("pagination diagnostics=%v, want %v", got, want)
		}
	}
}
