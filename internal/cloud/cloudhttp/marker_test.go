package cloudhttp

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// The client and a lease print as exactly their markers under every verb,
// in slog's text and JSON handlers, and inside a wrapped error.
func TestFormatterMarkers(t *testing.T) {
	client := &Client{token: "opaque-token-sentinel", source: "source-sentinel"}
	for v, marker := range map[any]string{client: "[cloud HTTPS client]", &Lease{client: client}: "[cloud lease]"} {
		for _, f := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			if got := fmt.Sprintf(f, v); got != marker {
				t.Errorf("%s = %q; want %q", f, got, marker)
			}
		}
		if got := fmt.Errorf("stop: %v", v).Error(); got != "stop: "+marker {
			t.Errorf("wrapped = %q", got)
		}
		var logs bytes.Buffer
		slog.New(slog.NewTextHandler(&logs, nil)).Info("x", "v", v)
		slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", "v", v)
		if !strings.Contains(logs.String(), `v="`+marker+`"`) || !strings.Contains(logs.String(), `"v":"`+marker+`"`) || strings.Contains(logs.String(), "sentinel") {
			t.Errorf("logs = %s", &logs)
		}
	}
}
