package cloud

import (
	"errors"
	"strings"
	"testing"
)

func TestOnlineLicenseMissing(t *testing.T) {
	for _, tc := range []struct{ name, member string }{
		{"missing", ""},
		{"null", `"sdkLicenseKey":null,`},
		{"empty", `"sdkLicenseKey":"",`},
		{"whitespace", `"sdkLicenseKey":" \t\r\n\u2003",`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"code":200,"data":{"conId":"cleanup-id","deviceUid":"uid",` + tc.member + `"username":"user","password":"pass"}}`
			a, ids, err := ParseOnline([]byte(body))
			var v *ValidationError
			if !errors.As(err, &v) || v.Field != "data.sdkLicenseKey" || v.Rule != "license_missing" {
				t.Errorf("got %v, want data.sdkLicenseKey/license_missing", err)
			}
			if !errors.Is(err, ErrLicenseMissing) || errors.Is(err, ErrInvalid) || Disposition(err) != Malformed {
				t.Errorf("got invalid=%v disposition=%v, want distinct nonretryable malformed outcome", errors.Is(err, ErrInvalid), Disposition(err))
			}
			if a.ConnectionID() != "" || len(ids) != 1 || ids[0].Value() != "cleanup-id" {
				t.Error("want no allocation and one retained cleanup identifier")
			}
		})
	}
}

func TestOnlineMalformedLicenseUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, value, field, rule string
		cleanup                  bool
	}{
		{"nul", `"PRIVATE_LICENSE\u0000TAIL"`, "data.sdkLicenseKey", "nonblank_string_without_nul", true},
		{"number", `123`, "body", "field_type", true},
		{"object", `{}`, "body", "field_type", true},
		{"oversized", `"` + strings.Repeat("x", MaxBody) + `"`, "body", "body_limit", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(onlineFixture, `"opaque-license"`, tc.value, 1)
			a, ids, err := ParseOnline([]byte(body))
			var v *ValidationError
			if !errors.Is(err, ErrInvalid) || !errors.As(err, &v) || v.Field != tc.field || v.Rule != tc.rule {
				t.Errorf("got %v, want ErrInvalid with %s/%s", err, tc.field, tc.rule)
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE_") {
				t.Error("error contains license material")
			}
			if a.ConnectionID() != "" || (len(ids) == 1) != tc.cleanup || tc.cleanup && ids[0].Value() != "allocation-secret" {
				t.Error("allocation or cleanup identifier behavior changed")
			}
		})
	}
}
