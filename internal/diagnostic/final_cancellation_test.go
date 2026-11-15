package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestFinalCancellationWithoutIndependentCause(t *testing.T) {
	unknown := errors.New("UNKNOWN_TEXT")
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"plain", context.Canceled, ""},
		{"wrapped", fmt.Errorf("private wrapper: %w", context.Canceled), ""},
		{"generic transport", errors.Join(context.Canceled, cloud.ErrTransport, session.ErrTransport), ""},
		{"typed", errors.Join(context.Canceled, NewTerminal(session.Cloud, "canceled", session.ErrTransport)), ""},
		{"timeout", errors.Join(context.Canceled, session.ErrTimeout), "timeout"},
		{"offline", errors.Join(context.Canceled, cloud.ErrOffline), "device_offline"},
		{"unknown", errors.Join(context.Canceled, unknown), "operation_failed"},
		{"typed with unknown sibling", errors.Join(NewTerminal(session.Cloud, "canceled", session.ErrTransport), unknown), "operation_failed"},
		{"typed with timeout sibling", errors.Join(NewTerminal(session.Cloud, "canceled", session.ErrTransport), session.ErrTimeout), "timeout"},
		{"blocking policy", NewTerminal(session.Cloud, "canceled", session.ErrUncertain), "ownership_uncertain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			LogFinal(slog.New(slog.NewJSONHandler(&logs, nil)), "proxy failed", "session", tc.err, nil)
			if tc.want == "" {
				if logs.Len() != 0 {
					t.Fatalf("cancellation-only report: %s", &logs)
				}
			} else if strings.Count(logs.String(), `"level":"ERROR"`) != 1 || !strings.Contains(logs.String(), `"reason":"`+tc.want+`"`) ||
				!strings.Contains(logs.String(), `"error":`) || strings.Contains(tc.err.Error(), "UNKNOWN_TEXT") != strings.Contains(logs.String(), "UNKNOWN_TEXT") {
				t.Fatalf("report=%s want one ERROR with reason %s and the error's text", &logs, tc.want)
			}
		})
	}
}
