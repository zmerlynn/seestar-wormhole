package cloud

import (
	"errors"
	"strings"
	"testing"
)

func TestCredentialFailureSetting(t *testing.T) {
	for _, tc := range []struct{ email, password, field string }{
		{"invalid", "valid-password", "email"},
		{strings.Repeat("x", 4097), "valid-password", "email"},
		{"\xff@a.invalid", "valid-password", "email"},
		{"a@b.invalid", "short", "password"},
		{"a@b.invalid", strings.Repeat("x", 33), "password"},
		{"a@b.invalid", "valid-password\xff", "password"},
	} {
		_, err := EmailCredentials(tc.email, tc.password)
		var failure *ValidationError
		if !errors.Is(err, ErrConfig) || !errors.As(err, &failure) || failure.Field != tc.field {
			t.Fatalf("setting=%s error=%v", tc.field, err)
		}
	}
}
