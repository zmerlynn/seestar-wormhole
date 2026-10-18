package cloudhttp

import (
	"reflect"
	"testing"
)

func TestCloudOptionalEnvelopeLiterals(t *testing.T) {
	for _, body := range []string{
		`{"data":{"conId":"synthetic-conid-sentinel","deviceUid":"SYNTHETICUID00000001","sdkLicenseKey":"synthetic-license-sentinel","username":"synthetic-user-sentinel","password":"synthetic-password-sentinel"}}`,
		`{"code":null,"msg":null,"data":{"conId":"synthetic-conid-sentinel","deviceUid":"SYNTHETICUID00000001","sdkLicenseKey":"synthetic-license-sentinel","username":"synthetic-user-sentinel","password":"synthetic-password-sentinel"}}`,
	} {
		want, _ := cloudProjection("allocation", []byte(metamorphicFixtures[5].body))
		got, err := cloudProjection("allocation", []byte(body))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("optional envelope changed allocation: %v", err)
		}
	}
	for _, body := range []string{`{}`, `{"code":null,"msg":null}`} {
		if _, err := cloudProjection("stop", []byte(body)); err != nil {
			t.Fatalf("optional envelope changed stop: %v", err)
		}
	}
}
