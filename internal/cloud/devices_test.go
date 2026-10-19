package cloud

import (
	"strings"
	"testing"
)

const devicesFixture = `{"code":200,"msg":"ok","data":{"items":[{"deviceSn":"serial-sentinel-1234","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"synthetic-wifi"},{"deviceSn":"serial-sentinel-5678","deviceModel":"S30","deviceStatus":4,"ssid":"synthetic-wifi"}],"current":1,"total":2}}`

func TestParseDevices(t *testing.T) {
	for _, tc := range []struct {
		body         string
		count, total int
	}{
		{devicesFixture, 2, 2},
		{`{"code":200,"msg":"ok","data":{"items":[],"total":0}}`, 0, 0},
		{`{"code":200,"msg":"ok","data":{"items":null,"total":0}}`, 0, 0},
		{`{"code":200,"msg":"ok","data":{"total":0}}`, 0, 0},
		{`{"code":200,"msg":"ok","data":{"items":[{"deviceSn":"serial","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1}]}}`, 1, -1},
	} {
		ds, total, err := ParseDevices([]byte(tc.body))
		if err != nil || len(ds) != tc.count || total != tc.total {
			t.Fatalf("got count=%d total=%d error=%v", len(ds), total, err)
		}
		if len(ds) > 0 && (ds[0].Serial() == "" || ds[0].Model() == "") {
			t.Fatal("missing identity")
		}
	}
}

func TestParseDevicesInvalid(t *testing.T) {
	for _, b := range []string{
		strings.Replace(devicesFixture, `"total":2`, `"total":2.5`, 1),
		strings.Replace(devicesFixture, `"deviceSn":"serial-sentinel-1234"`, `"deviceSn":null`, 1),
		strings.Replace(devicesFixture, `"deviceModel":"S50 Pro"`, `"deviceModel":""`, 1),
		devicesFixture + `{}`, strings.Repeat(" ", MaxBody) + devicesFixture,
	} {
		if _, _, err := ParseDevices([]byte(b)); err == nil {
			t.Fatalf("accepted malformed list %q", b[:min(len(b), 250)])
		}
	}
}

func FuzzDevices(f *testing.F) {
	f.Add([]byte(devicesFixture))
	f.Add([]byte(`{"data":{"items":[{"deviceSn":"synthetic-a","deviceModel":"S50","deviceStatus":2,"ssid":"first"},{"deviceSn":"synthetic-a","deviceModel":"S30","deviceStatus":1,"ssid":"second"}]}}`))
	f.Add([]byte(observedDevicesFixture))
	f.Add([]byte(unknownDevicesFixture))
	f.Add([]byte(`{"code":200,"msg":"ok","data":{"items":null,"total":0}}`))
	f.Add([]byte(`{"code":200,"msg":"ok","data":{"items":[],"total":0}}`))
	for _, tc := range catalogItemLiterals {
		f.Add([]byte(tc.body))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		ds, _, err := ParseDevices(b)
		if err == nil {
			if len(ds) > 256 {
				t.Fatal("catalog entry limit exceeded")
			}
			seen := map[[2]string]bool{}
			for _, d := range ds {
				if d.Serial() == "" || d.Model() == "" || seen[[2]string{d.Serial(), d.Model()}] {
					t.Fatal("invalid identity")
				}
				seen[[2]string{d.Serial(), d.Model()}] = true
			}
		}
	})
}
