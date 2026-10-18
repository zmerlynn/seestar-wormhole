package cloud

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Literal fixtures independently capture the consumed DTO fields.
const onlineFixture = `{"code":200,"data":{"conId":"allocation-secret","deviceUid":"uid-secret","sdkLicenseKey":"opaque-license","username":"account-secret","password":"password-secret","p2pFactory":"TUTK","p2pLicenseKey":"unused","p2pStatus":1},"msg":"ok"}`
const detailFixture = `{"code":200,"data":{"deviceSn":"serial","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"Scope","location":{"city":"somewhere"}},"msg":"ok"}`

func TestLiteralParsers(t *testing.T) {
	a, ids, err := ParseOnline([]byte(onlineFixture))
	if err != nil || len(ids) != 1 || a.ConnectionID() != "allocation-secret" || a.UID() != "uid-secret" || a.Username() != "account-secret" || a.Password() != "password-secret" || string(a.License()) != "opaque-license" {
		t.Fatalf("online fixture failed: %v", err)
	}
	b := a.License()
	b[0] = 'X'
	if string(a.License()) != "opaque-license" {
		t.Fatal("license alias")
	}
	d, err := ParseDetail([]byte(detailFixture))
	if err != nil || d.Serial() != "serial" || d.Model() != "S50 Pro" || d.Status() != 1 {
		t.Fatalf("detail: %v", err)
	}
	for _, v := range []any{a, d, ids[0]} {
		for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
			s := fmt.Sprintf(f, v)
			if strings.Contains(s, "secret") || strings.Contains(s, "serial") {
				t.Fatal("format leaked")
			}
		}
	}
}

func TestStrictEnvelopesAndRecovery(t *testing.T) {
	for _, s := range []string{
		``, `null`, `[]`, `{}`, `{"code":200,"data":{},"msg":null}`,
		strings.Replace(onlineFixture, `"code":200`, `"code":200.0`, 1),
		strings.Replace(onlineFixture, `"password":"password-secret"`, `"password":null`, 1),
		onlineFixture + `{}`, strings.Repeat(" ", MaxBody+1),
	} {
		if _, _, err := ParseOnline([]byte(s)); err == nil {
			t.Fatalf("accepted malformed fixture %.80q", s)
		}
	}
	// An identifier in a message, nested object, or incomplete string is not an allocation.
	for _, s := range []string{`{"msg":"conId"}`, `{"data":{"nested":{"conId":"wrong"}}}`, `{"data":{"conId":"unfinished`} {
		_, ids, _ := ParseOnline([]byte(s))
		if len(ids) != 0 {
			t.Fatal("spurious allocation")
		}
	}
	for _, code := range []string{"300403", "300404"} {
		_, _, err := ParseOnline([]byte(`{"code":` + code + `,"data":null,"msg":"account-secret"}`))
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("busy: %v", err)
		}
	}
}

func FuzzJSON(f *testing.F) {
	for _, s := range []string{onlineFixture, detailFixture, unknownDetailFixture,
		`{"code":300404,"data":{"deviceSn":"synthetic-a","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"}}`,
		`{"code":200,"data":{"deviceSn":"SN","deviceModel":"S50","deviceStatus":1,"ssid":"Scope"}}`,
		`{"code":200}`, `{"code":300403,"data":null}`, `{"code":200,"data":null}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		a, ids, err := ParseOnline(b)
		if len(ids) > MaxBody/8 {
			t.Fatal("identifier budget")
		}
		if err == nil && (a.ConnectionID() == "" || len(ids) != 1) {
			t.Fatal("invalid success")
		}
		ParseDetail(b)
		ParseLANDetail(b)
		ParseStop(b)
	})
}

func TestEnvelopeCodeIsNotASuccessWhitelist(t *testing.T) {
	// HTTP success does not require a particular envelope code.
	// A structurally valid payload remains mandatory. Known refusal codes override.
	for _, code := range []string{"0", "123"} {
		if _, _, err := ParseOnline([]byte(strings.Replace(onlineFixture, `"code":200`, `"code":`+code, 1))); err != nil {
			t.Fatalf("invented success whitelist: %v", err)
		}
	}
}
func TestStrictDetailAndStop(t *testing.T) {
	for _, s := range []string{
		strings.Replace(detailFixture, `"deviceStatus":1`, `"deviceStatus":4`, 1),
		strings.Replace(detailFixture, `"deviceStatus":1`, `"deviceStatus":2`, 1),
		strings.Replace(detailFixture, `"deviceStatus":1`, `"deviceStatus":0`, 1),
		strings.Replace(detailFixture, `"deviceStatus":1`, `"deviceStatus":1.5`, 1),
	} {
		if _, err := ParseDetail([]byte(s)); err == nil {
			t.Fatal("accepted malformed or unavailable detail")
		}
	}
	for _, s := range []string{`null`, `{"code":false,"data":null}`, `{"code":200,"data":null,"msg":"ok"} trailing`} {
		if ParseStop([]byte(s)) == nil {
			t.Fatal("invalid stop")
		}
	}
}
