package cloud

import (
	"testing"
)

func TestCloudDuplicateNames(t *testing.T) {
	for name, parse := range responseParsers() {
		t.Run(name, func(t *testing.T) {
			// One literal body satisfies every consumed schema; unknown fields are allowed.
			if err := parse([]byte(`{"code":200,"data":{"accessToken":"token","id":"profile","items":[],"deviceSn":"serial","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"Scope","conId":"allocation","deviceUid":"uid","sdkLicenseKey":"license","username":"user","password":"pass"}}`)); err != nil {
				t.Fatalf("ordinary union fixture: %v", err)
			}
			for _, frame := range []string{
				`{"code":200,"code":200,"data":{"accessToken":"token","id":"profile","items":[],"deviceSn":"serial","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"Scope","conId":"allocation","deviceUid":"uid","sdkLicenseKey":"license","username":"user","password":"pass"}}`,
				`{"code":200,"co\u0064e":200,"data":{"accessToken":"token","id":"profile","items":[],"deviceSn":"serial","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"Scope","conId":"allocation","deviceUid":"uid","sdkLicenseKey":"license","username":"user","password":"pass"}}`,
				`{"code":200,"future":{"a":1,"a":2},"data":{"accessToken":"token","id":"profile","items":[],"deviceSn":"serial","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"Scope","conId":"allocation","deviceUid":"uid","sdkLicenseKey":"license","username":"user","password":"pass"}}`,
			} {
				if err := parse([]byte(frame)); err == nil {
					t.Fatalf("accepted duplicate: %s", frame)
				}
			}
		})
	}
}
func TestCloudCaseMatching(t *testing.T) {
	token, err := ParseLogin(200, []byte(`{"code":200,"data":{"accessToken":"exact","ACCESSTOKEN":"wrong"},"CODE":"wrong"}`), false)
	if err != nil || token.Value() != "exact" {
		t.Fatalf("case matching: %v, %v", token, err)
	}
	if _, err = ParseLogin(200, []byte(`{"code":200,"data":{"ACCESSTOKEN":"wrong"}}`), false); err == nil {
		t.Fatal("accepted wrong-case token")
	}
}
