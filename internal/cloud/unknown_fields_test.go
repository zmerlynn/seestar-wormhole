package cloud

import (
	"errors"
	"strings"
	"testing"
)

// Synthetic literal: observed HTTP/envelope 200, 695-byte token, two numeric
// metadata fields and one undocumented string field. extraDemo is invented.
const observedLoginFixture = `{"code":200,"data":{"accessToken":"sssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss","accessExpire":1234567890,"refreshAfter":3600,"extraDemo":"synthetic"}}`
const unknownDetailFixture = `{"code":200,"msg":"ok","extra":{"nested":[{"code":false}]},"data":{"deviceSn":"serial","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"Scope","extra":null,"location":{"city":"somewhere","extra":{"city":false}}}}`
const unknownDevicesFixture = `{"code":200,"msg":"ok","extra":true,"data":{"extra":[],"items":[{"deviceSn":"serial","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"Scope","extra":123,"location":{"city":"somewhere","extra":false}}],"attributes":{"deviceSn":"other","deviceModel":"S30","extra":{},"location":{"extra":null}}}}`

func TestObservedLoginUnknownField(t *testing.T) {
	for _, renewal := range []bool{false, true} {
		token, err := ParseLogin(200, []byte(observedLoginFixture), renewal)
		if err != nil || token.Value() != strings.Repeat("s", 695) {
			t.Errorf("renewal=%t: token length=%d err=%v, want synthetic 695-byte token", renewal, len(token.Value()), err)
		}
	}
}

func responseParsers() map[string]func([]byte) error {
	return map[string]func([]byte) error{
		"login":   func(b []byte) error { _, err := ParseLogin(200, b, false); return err },
		"renewal": func(b []byte) error { _, err := ParseLogin(201, b, true); return err },
		"profile": func(b []byte) error { return ParseProfile(200, b) },
		"devices": func(b []byte) error { _, _, err := ParseDevices(b); return err },
		"detail":  func(b []byte) error { _, err := ParseDetail(b); return err },
		"online":  func(b []byte) error { _, _, err := ParseOnline(b); return err },
		"stop":    ParseStop,
	}
}

func responseFixtures() map[string]string {
	return map[string]string{
		"login": observedLoginFixture, "renewal": observedLoginFixture,
		"profile": `{"code":200,"msg":null,"data":{"id":"profile","extra":{"id":false}}}`,
		"devices": unknownDevicesFixture, "detail": unknownDetailFixture,
		"online": `{"code":200,"msg":"ok","data":{"conId":"allocation","deviceUid":"uid","sdkLicenseKey":"license","username":"user","password":"password","extra":{"conId":"nested"}}}`,
		"stop":   `{"code":200,"msg":"ok","data":{"extra":[{"extra":null}]}}`,
	}
}

func TestUnknownResponseFields(t *testing.T) {
	fixtures := responseFixtures()
	for name, parse := range responseParsers() {
		t.Run(name, func(t *testing.T) {
			// Reuse documented names inside unknown objects to ensure their types
			// are ignored there, rather than treated as fields of the enclosing DTO.
			body := strings.Replace(fixtures[name], `"code":200`, `"code":200,"future":{"code":false,"data":[{"accessToken":123}]}`, 1)
			if err := parse([]byte(body)); err != nil {
				t.Fatalf("unknown fields: %v", err)
			}
		})
	}
}

func TestUnknownFieldsKeepJSONGuards(t *testing.T) {
	fixtures := responseFixtures()
	for name, parse := range responseParsers() {
		t.Run(name, func(t *testing.T) {
			body := fixtures[name]
			cases := map[string]string{
				"trailing JSON": body + `{}`,
				"trailing text": body + ` trailing`,
				"body limit":    strings.Repeat(" ", MaxBody) + body,
			}
			for rule, malformed := range cases {
				t.Run(rule, func(t *testing.T) {
					if err := parse([]byte(malformed)); !errors.Is(err, ErrInvalid) {
						t.Fatalf("got %v, want ErrInvalid", err)
					}
				})
			}
		})
	}
}

func TestUnknownFieldsIgnoreUnusedTokenMetadata(t *testing.T) {
	for _, renewal := range []bool{false, true} {
		for _, field := range []string{"accessExpire", "refreshAfter"} {
			for _, value := range []string{`null`, `"1"`, `true`, `1.5`, `2147483648`, `-2147483649`} {
				body := `{"code":200,"data":{"accessToken":"opaque","extra":{},"` + field + `":` + value + `}}`
				if _, err := ParseLogin(200, []byte(body), renewal); err != nil {
					t.Errorf("%s=%s renewal=%t: got %v, want success", field, value, renewal, err)
				}
			}
			for _, value := range []string{`-2147483648`, `2147483647`} {
				body := `{"code":200,"data":{"accessToken":"opaque","extra":{},"` + field + `":` + value + `}}`
				if _, err := ParseLogin(200, []byte(body), renewal); err != nil {
					t.Errorf("%s=%s renewal=%t: %v", field, value, renewal, err)
				}
			}
		}
	}
	for _, body := range []string{
		strings.Replace(unknownDetailFixture, `"deviceModel":"S50 Pro"`, `"deviceModel":null`, 1),
	} {
		if _, err := ParseDetail([]byte(body)); !errors.Is(err, ErrInvalid) {
			t.Errorf("detail: got %v, want ErrInvalid", err)
		}
	}
}
