package cloud

import (
	"errors"
	"fmt"
	"testing"
)

// These semantic expectations are literal and independent of the decoder and
// the encoder. The total is metadata; identities come from actual entries.
var catalogItemLiterals = []struct {
	name, body string
	want       [][2]string
	total      int
}{
	{"stale total", `{"code":200,"data":{"total":0,"items":[{"deviceSn":"a","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1}]}}`, [][2]string{{"a", "S50 Pro"}}, -1},
	{"negative total", `{"code":200,"data":{"total":-7,"items":[{"deviceSn":"a","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1}]}}`, [][2]string{{"a", "S50 Pro"}}, -1},
	{"equal identities", `{"code":200,"data":{"total":2,"items":[{"deviceSn":"a","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"},{"deviceSn":"a","deviceModel":"S50 Pro","deviceStatus":3,"ssid":"synthetic-wifi"}]}}`, [][2]string{{"a", "S50 Pro"}}, 2},
	{"understated duplicate count", `{"data":{"total":1,"items":[{"deviceSn":"a","deviceModel":"S50 Pro","ssid":"first","deviceStatus":1},{"deviceSn":"a","deviceModel":"S50 Pro","ssid":"second","deviceStatus":2}]}}`, [][2]string{{"a", "S50 Pro"}}, -1},
	{"null items", `{"code":200,"data":{"items":null}}`, nil, -1},
	{"absent items", `{"code":200,"data":{}}`, nil, -1},
	{"null items positive hint", `{"code":200,"data":{"total":12,"items":null}}`, nil, 12},
	{"absent items positive hint", `{"code":200,"data":{"total":12}}`, nil, 12},
	{"null total", `{"code":200,"data":{"total":null,"items":[{"deviceSn":"b","deviceModel":"S30","ssid":"synthetic-wifi","deviceStatus":1}]}}`, [][2]string{{"b", "S30"}}, -1},
	{"large hint", `{"code":200,"data":{"total":999999,"items":[{"deviceSn":"b","deviceModel":"S30","ssid":"synthetic-wifi","deviceStatus":1}]}}`, [][2]string{{"b", "S30"}}, 999999},
	{"stable order", `{"code":200,"data":{"total":0,"items":[{"deviceSn":"b","deviceModel":"S30","ssid":"synthetic-wifi","deviceStatus":1},{"deviceSn":"a","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1},{"deviceSn":"b","deviceModel":"S30","ssid":"synthetic-wifi","deviceStatus":1}]}}`, [][2]string{{"b", "S30"}, {"a", "S50 Pro"}}, -1},
}

func TestCatalogItemLiterals(t *testing.T) {
	for _, tc := range catalogItemLiterals {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := ParseDevices([]byte(tc.body))
			if err != nil || len(got) != len(tc.want) || total != tc.total {
				t.Fatalf("count=%d total=%d error=%v, want count=%d total=%d", len(got), total, err, len(tc.want), tc.total)
			}
			for i, want := range tc.want {
				if got[i].Serial() != want[0] || got[i].Model() != want[1] {
					t.Errorf("identity[%d] differs from literal expectation", i)
				}
			}
		})
	}
}

func TestCatalogItemSafety(t *testing.T) {
	for _, body := range []string{
		`{"code":200,"data":{"total":0,"items":[null]}}`,
		`{"code":200,"data":{"items":[{"deviceSn":null,"deviceModel":"S50","ssid":"synthetic-wifi","deviceStatus":1}]}}`,
		`{"code":200,"data":{"items":[{"deviceSn":123,"deviceModel":"S50","ssid":"synthetic-wifi","deviceStatus":1}]}}`,
		`{"code":200,"data":{"items":[{"deviceModel":"S50"}]}}`,
		`{"code":200,"data":{"items":[{"deviceSn":" ","deviceModel":"S50","ssid":"synthetic-wifi","deviceStatus":1}]}}`,
		`{"code":200,"data":{"items":[{"deviceSn":"a\u0000","deviceModel":"S50","ssid":"synthetic-wifi","deviceStatus":1}]}}`,
		`{"code":200,"data":{"items":[{"deviceSn":"a","deviceModel":null,"ssid":"synthetic-wifi","deviceStatus":1}]}}`,
		`{"code":200,"data":{"items":[{"deviceSn":"a","deviceModel":false,"ssid":"synthetic-wifi","deviceStatus":1}]}}`,
		`{"code":200,"data":{"items":[{"deviceSn":"a"}]}}`,
		`{"code":200,"data":{"items":[{"deviceSn":"a","deviceModel":"","ssid":"synthetic-wifi","deviceStatus":1}]}}`,
		`{"code":200,"data":{"items":[{"deviceSn":"a","deviceModel":" \t","ssid":"synthetic-wifi","deviceStatus":1}]}}`,
		`{"code":200,"data":{"items":[{"deviceSn":"a","deviceModel":"S50\u0000","ssid":"synthetic-wifi","deviceStatus":1}]}}`,
		`{"code":200,"data":{"total":0,"items":{}}}`,
		`{"code":200,"data":{"total":1.5,"items":[]}}`,
		`{"code":200,"data":{"total":"1","items":[]}}`,
		`{"code":200,"data":{"total":9223372036854775808,"items":[]}}`,
	} {
		got, _, err := ParseDevices([]byte(body))
		if !errors.Is(err, ErrInvalid) || len(got) != 0 {
			t.Errorf("unsafe catalog returned count=%d error=%v", len(got), err)
		}
	}
	for _, code := range []struct {
		body string
		want error
	}{
		{`{"code":300403,"data":{"items":[]}}`, ErrBusy},
		{`{"code":300406,"data":{"items":[]}}`, ErrOffline},
		{`{"code":300405,"data":{"items":[]}}`, ErrRejected},
	} {
		if _, _, err := ParseDevices([]byte(code.body)); !errors.Is(err, code.want) {
			t.Errorf("refusal = %v, want %v", err, code.want)
		}
	}
}

func TestCatalogItemLimit(t *testing.T) {
	for _, tc := range []struct {
		count            int
		same, sameSerial bool
		want             int
	}{
		{256, false, false, 256},
		{257, false, false, 0},
		{257, true, false, 1},
		{256, false, true, 256},
		{257, false, true, 0},
	} {
		body := `{"code":200,"data":{"items":[`
		for i := 0; i < tc.count; i++ {
			if i > 0 {
				body += ","
			}
			serial := i
			if tc.same {
				serial = 0
			}
			if tc.sameSerial {
				body += fmt.Sprintf(`{"deviceSn":"synthetic-only-serial","deviceModel":"model-%d","ssid":"synthetic-wifi","deviceStatus":1}`, i)
			} else {
				body += fmt.Sprintf(`{"deviceSn":"s%d","deviceModel":"S50","ssid":"synthetic-wifi","deviceStatus":1}`, serial)
			}
		}
		body += `]}}`
		got, _, err := ParseDevices([]byte(body))
		if tc.want == 0 {
			if !errors.Is(err, ErrInvalid) || len(got) != 0 {
				t.Errorf("over-limit catalog: count=%d error=%v", len(got), err)
			}
		} else if err != nil || len(got) != tc.want {
			t.Errorf("%d entries same=%v: count=%d error=%v, want %d", tc.count, tc.same, len(got), err, tc.want)
		}
	}
}
