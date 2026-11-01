package cloudhttp

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Synthetic literals exercise both the page decoder and cross-page aggregation.
const pairPage = `{"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"first"},{"deviceSn":"private-a","deviceModel":"S30","deviceStatus":1,"ssid":"second"}]}}`
const pairLaterPage = `{"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S30","deviceStatus":4,"ssid":"later"}]}}`

func TestCatalogPairSelection(t *testing.T) {
	for _, pages := range [][]string{{pairPage}, {pageA2, pairLaterPage}} {
		for _, tc := range []struct {
			name           string
			input, want    Device
			option, reason string
		}{
			{"automatic ambiguous", Device{}, Device{}, "--serial", "multiple devices on account"},
			{"model alone ambiguous", Device{Model: "S30"}, Device{}, "--serial", "multiple devices on account"},
			{"serial ambiguous", Device{Serial: "private-a"}, Device{}, "--model", "name --model"},
			{"first pair", Device{"private-a", "S50 Pro"}, Device{"private-a", "S50 Pro"}, "", ""},
			{"second pair", Device{"private-a", "S30"}, Device{"private-a", "S30"}, "", ""},
			{"absent model", Device{"private-a", "S50"}, Device{}, "--model", "pair not found"},
			{"exact model case", Device{"private-a", "s30"}, Device{}, "--model", "pair not found"},
			{"absent serial", Device{"private-missing", "S30"}, Device{}, "--serial", "--serial not found"},
		} {
			t.Run(fmt.Sprintf("%s/%d pages", tc.name, len(pages)), func(t *testing.T) {
				var logs bytes.Buffer
				f := &selectionCloud{bodies: append([]string(nil), pages...)}
				c := selectionClient(t, f, &logs, slog.LevelDebug)
				got, err := c.SelectDevice(t.Context(), tc.input)
				if got != tc.want {
					t.Fatalf("selection=%v, want %v", got, tc.want)
				}
				if tc.option == "" {
					if err != nil {
						t.Fatal(err)
					}
				} else {
					var option *diagnostic.OptionError
					if !errors.Is(err, cloud.ErrConfig) || !errors.As(err, &option) || option.Option != tc.option || !strings.Contains(err.Error(), tc.reason) {
						t.Fatalf("error=%v, want config/%s/%s", err, tc.option, tc.reason)
					}
					diagnostic.Log(c.logger, "selection failed", "selection", err)
					if !strings.Contains(logs.String(), tc.reason) {
						t.Fatal("missing actionable selection diagnostic")
					}
					if strings.Contains(logs.String(), "device_selected") {
						t.Fatal("selected ambiguous or missing pair")
					}
					assertCatalogPagePrivacy(t, err.Error())
				}
				if tc.name == "absent serial" {
					assertCatalogPageRequests(t, f, len(pages), "/v1/my-device/detail")
				} else {
					assertCatalogPageRequests(t, f, len(pages))
				}
				assertCatalogPagePrivacy(t, logs.String())
			})
		}
	}
}

func TestCatalogPairUnrelatedSerial(t *testing.T) {
	var logs bytes.Buffer
	f := &selectionCloud{bodies: []string{`{"data":{"total":3,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"first"},{"deviceSn":"private-a","deviceModel":"S30","deviceStatus":2,"ssid":"second"},{"deviceSn":"private-b","deviceModel":"S30","deviceStatus":4,"ssid":"third"}]}}`}}
	c := selectionClient(t, f, &logs, slog.LevelDebug)
	got, err := c.SelectDevice(t.Context(), Device{Serial: "private-b"})
	if err != nil || got != (Device{"private-b", "S30"}) {
		t.Fatalf("selection=%v error=%v, want unique unrelated serial", got, err)
	}
	assertCatalogPageRequests(t, f, 1)
	assertCatalogPagePrivacy(t, logs.String())
}

func TestCatalogPairCapacity(t *testing.T) {
	// Generated synthetic size boundaries complement the literal two-pair cases.
	// One serial with many models must consume the same budget as many serials.
	page := func(start, end int) string {
		body := `{"data":{"total":257,"items":[`
		for i := start; i < end; i++ {
			if i > start {
				body += ","
			}
			body += fmt.Sprintf(`{"deviceSn":"private-a","deviceModel":"synthetic-model-%d","deviceStatus":1,"ssid":"synthetic"}`, i)
		}
		return body + `]}}`
	}
	for _, count := range []int{256, 257} {
		var logs bytes.Buffer
		pages := []string{page(0, 100), page(100, 200), page(200, count)}
		if count == 256 {
			pages = append(pages, page(0, 1))
		}
		f := &selectionCloud{bodies: pages}
		c := selectionClient(t, f, &logs, slog.LevelDebug)
		got, _, _, err := c.devices(t.Context())
		if count == 256 {
			if err != nil || len(got) != 256 {
				t.Fatalf("count=%d error=%v, want 256 pairs", len(got), err)
			}
			if !strings.Contains(logs.String(), `"duplicate_items":1`) {
				t.Fatal("duplicate at pair capacity not logged")
			}
		} else if !errors.Is(err, cloud.ErrInvalid) || got != nil {
			t.Fatalf("count=%d error=%v, want no partial over-limit catalog", len(got), err)
		}
		assertCatalogPageRequests(t, f, len(pages))
		assertCatalogPagePrivacy(t, logs.String())
	}
}

func TestCatalogPairDuplicateBoundaries(t *testing.T) {
	for _, pages := range [][]string{
		{`{"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"first"},{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"second"}]}}`},
		{pageA2, `{"data":{"total":2,"items":[{"deviceSn":"private-a","deviceModel":"S50 Pro","deviceStatus":4,"ssid":"later"}]}}`},
	} {
		var logs bytes.Buffer
		f := &selectionCloud{bodies: append([]string(nil), pages...)}
		c := selectionClient(t, f, &logs, slog.LevelDebug)
		got, err := c.SelectDevice(t.Context(), Device{Model: "S50 Pro"})
		if err != nil || got != (Device{"private-a", "S50 Pro"}) {
			t.Fatalf("selection=%v error=%v, want unique pair", got, err)
		}
		if !strings.Contains(logs.String(), `"duplicate_items":1`) {
			t.Fatal("duplicate pair not logged")
		}
		assertCatalogPageRequests(t, f, len(pages))
		assertCatalogPagePrivacy(t, logs.String())
	}
}
