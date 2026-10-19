package cloud

import (
	"errors"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"strings"
	"testing"
)

// Literal field set/types with synthetic values. Redacted unknown keys are
// replaced by invented keys of the observed lengths (8 and 10).
const observedDevicesFixture = `{"code":200,"data":{"current":1,"items":[{"address":"somewhere","createTime":1720000000,"deviceAlias":"","deviceId":42,"deviceModel":"Synthetic Model","deviceSn":"SN000001","deviceStatus":1,"id":7,"isShared":false,"latitude":0,"longitude":0,"shareExpirationTime":0,"shareStartTime":0,"shareStatus":0,"ssid":"synthetic-wifi00","subModel":"","timezone":"Synthetic/Zone0","transferStatus":"","futureDemo":1,"updateTime":1720000001,"userId":"100000000000000001"}],"total":1,"pageDemo":100},"msg":"ok"}`

func TestObservedDevices(t *testing.T) {
	ds, total, err := ParseDevices([]byte(observedDevicesFixture))
	if err != nil || len(ds) != 1 || total != 1 {
		t.Fatalf("observed list: count=%d total=%d err=%v", len(ds), total, err)
	}
	if ds[0].Serial() != "SN000001" || ds[0].Model() != "Synthetic Model" {
		t.Fatal("observed identity lost")
	}
}

func TestCloudIgnoresEveryUnusedFieldType(t *testing.T) {
	cases := []struct {
		name, fixture, path, fields string
	}{
		{"devices", observedDevicesFixture, "root", "msg"},
		{"detail", detailFixture, "root", "msg"},
		{"online", onlineFixture, "root", "msg"},
		{"devices", observedDevicesFixture, "data", "id type attributes data list unReadCount pageDemo"},
		{"devices", observedDevicesFixture, "item", "address createTime deviceAlias deviceId id isShared latitude longitude shareExpirationTime shareStartTime shareStatus subModel timezone transferStatus futureDemo updateTime userId location"},
		{"detail", detailFixture, "data", "address createTime deviceAlias deviceId id isShared latitude longitude shareExpirationTime shareStartTime shareStatus subModel timezone transferStatus updateTime userId location"},
		{"online", onlineFixture, "data", "p2pFactory p2pLicenseKey p2pStatus"},
		{"stop", `{"code":200,"data":null}`, "root", "data msg"},
	}
	for _, tc := range cases {
		for _, field := range strings.Fields(tc.fields) {
			for _, value := range []string{`null`, `false`, `0`, `2147483648`, `1.5`, `""`, `"text"`, `[]`, `{}`} {
				t.Run(tc.name+"/"+tc.path+"/"+field+"/"+value, func(t *testing.T) {
					var root map[string]any
					if err := json.Unmarshal([]byte(tc.fixture), &root, json.MatchCaseInsensitiveNames(true)); err != nil {
						t.Fatal(err)
					}
					obj := root
					if tc.path != "root" {
						obj = root["data"].(map[string]any)
						if tc.path == "item" {
							obj = obj["items"].([]any)[0].(map[string]any)
						}
					}
					obj[field] = jsontext.Value(value)
					body, err := json.Marshal(root)
					if err != nil {
						t.Fatal(err)
					}
					if err := responseParsers()[tc.name](body); err != nil {
						t.Fatalf("unused field rejected: %v", err)
					}
				})
			}
		}
	}
}

func TestConsumedFieldsRemainRequired(t *testing.T) {
	cases := []struct{ name, fixture, fields string }{
		{"login", observedLoginFixture, "accessToken"},
		{"renewal", observedLoginFixture, "accessToken"},
		{"detail", detailFixture, "deviceSn deviceModel deviceStatus"},
		{"online", onlineFixture, "conId deviceUid sdkLicenseKey username password"},
	}
	for _, tc := range cases {
		for _, field := range strings.Fields(tc.fields) {
			for _, value := range []string{`null`, `false`, `""`, `[]`, `{}`} {
				var root map[string]any
				if err := json.Unmarshal([]byte(tc.fixture), &root, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Fatal(err)
				}
				root["data"].(map[string]any)[field] = jsontext.Value(value)
				body, err := json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				want := ErrInvalid
				if field == "sdkLicenseKey" && (value == `null` || value == `""`) {
					want = ErrLicenseMissing
				}
				if err := responseParsers()[tc.name](body); !errors.Is(err, want) {
					t.Errorf("%s %s=%s: got %v, want %v", tc.name, field, value, err, want)
				}
				delete(root["data"].(map[string]any), field)
				body, err = json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				if field == "sdkLicenseKey" {
					want = ErrLicenseMissing
				}
				if err := responseParsers()[tc.name](body); !errors.Is(err, want) {
					t.Errorf("%s missing %s: got %v, want %v", tc.name, field, err, want)
				}
			}
		}
	}
}
