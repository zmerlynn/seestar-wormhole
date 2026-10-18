package cloud

import (
	"errors"
	"strings"
	"testing"
)

func TestValidationRules(t *testing.T) {
	tests := []struct {
		name, body, field, rule string
		parse                   func([]byte) error
	}{
		{"login token", `{"code":0,"data":{"accessToken":123}}`, "data.accessToken", "string", func(b []byte) error { _, e := ParseLogin(200, b, false); return e }},
		{"profile code", `{"code":"secret","data":{"id":123}}`, "code", "integer", func(b []byte) error { return ParseProfile(200, b) }},
		{"list items", `{"code":0,"msg":"ok","data":{"items":"secret"}}`, "data.items", "array_or_zero_total", func(b []byte) error { _, _, e := ParseDevices(b); return e }},
		{"online password", strings.Replace(onlineFixture, `"password":"password-secret"`, `"password":123`, 1), "data.password", "nonblank_string_without_nul", func(b []byte) error { _, _, e := ParseOnline(b); return e }},
		{"stop code", `{"code":"secret","msg":"ok","data":null}`, "code", "integer", ParseStop},
		{"syntax", `{"code":`, "body", "json_syntax", ParseStop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.parse([]byte(tt.body))
			var v *ValidationError
			if !errors.Is(err, ErrInvalid) || !errors.As(err, &v) {
				t.Fatalf("got %v, want validation error wrapping ErrInvalid", err)
			}
			if v.Field != tt.field || v.Rule != tt.rule {
				t.Fatalf("got %s/%s, want %s/%s", v.Field, v.Rule, tt.field, tt.rule)
			}
		})
	}
}
