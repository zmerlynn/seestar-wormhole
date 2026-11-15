package cloud

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// assertMarker wants v printed as exactly marker under every verb, in slog's
// text and JSON handlers, and inside a wrapped error, with no secret.
func assertMarker(t *testing.T, v any, marker string, secrets ...string) {
	t.Helper()
	for _, f := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if got := fmt.Sprintf(f, v); got != marker {
			t.Errorf("%s = %q; want %q", f, got, marker)
		}
	}
	if got := fmt.Errorf("login: %v", v).Error(); got != "login: "+marker {
		t.Errorf("wrapped = %q", got)
	}
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("x", "v", v)
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", "v", v)
	if !strings.Contains(logs.String(), `v="`+marker+`"`) || !strings.Contains(logs.String(), `"v":"`+marker+`"`) {
		t.Errorf("logs = %s", &logs)
	}
	for _, secret := range secrets {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("%s leaked: %s", secret, &logs)
		}
	}
}

func TestFormatterMarkers(t *testing.T) {
	c, err := EmailCredentials("reader@example.invalid", "Example-Only-123")
	if err != nil {
		t.Fatal(err)
	}
	assertMarker(t, c, "[account credentials]", "Example-Only-123", "reader@example.invalid")
	assertMarker(t, AccessToken{value: "opaque-token-sentinel"}, "[account token]", "opaque-token-sentinel")
	m := NewAccountMachine(c)
	m.token = AccessToken{value: "opaque-token-sentinel"}
	assertMarker(t, m, "[account lifecycle]", "Example-Only-123", "opaque-token-sentinel")
}
