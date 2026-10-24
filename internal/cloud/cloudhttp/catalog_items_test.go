package cloudhttp

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"log/slog"
	"reflect"
	"testing"
)

func TestCatalogItemDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		body              string
		count, duplicates int
		ignored, empty    bool
	}{
		{`{"code":200,"data":{"total":0,"items":[{"deviceSn":"private-serial","deviceModel":"private-model","ssid":"synthetic-wifi","deviceStatus":1},{"deviceSn":"private-serial","deviceModel":"private-model","ssid":"synthetic-wifi","deviceStatus":1}]}}`, 1, 1, true, false},
		{`{"code":200,"data":{"items":null}}`, 0, 0, false, true},
	} {
		for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
			var logs bytes.Buffer
			c := selectionClient(t, &selectionCloud{bodies: []string{tc.body}}, &logs, level)
			got, _, _, err := c.devices(t.Context())
			if err != nil || len(got) != tc.count {
				t.Fatalf("count=%d error=%v, want count=%d", len(got), err, tc.count)
			}
			var records []map[string]any
			dec := jsontext.NewDecoder(&logs)
			for {
				var record map[string]any
				if err := json.UnmarshalDecode(dec, &record, json.MatchCaseInsensitiveNames(true)); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if record["msg"] == "cloud_catalog_oddities" {
					delete(record, "time")
					records = append(records, record)
				}
			}
			if level == slog.LevelInfo {
				if len(records) != 0 {
					t.Fatal("tolerated catalog oddity logged above debug")
				}
				continue
			}
			want := []map[string]any{{
				"level": "DEBUG", "msg": "cloud_catalog_oddities",
				"total_hint_ignored": tc.ignored, "empty_items": tc.empty,
				"duplicate_items": float64(tc.duplicates),
			}}
			if !reflect.DeepEqual(records, want) {
				t.Fatal("catalog diagnostic differs from fixed metadata expectation")
			}
		}
	}
}
