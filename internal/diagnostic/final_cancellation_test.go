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
	unknown := errors.New("PRIVATE_COORDINATES_TIME_TOKEN_UID_PAYLOAD")
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
			} else if strings.Count(logs.String(), `"level":"ERROR"`) != 1 || !strings.Contains(logs.String(), `"reason":"`+tc.want+`"`) {
				t.Fatalf("report=%s want one ERROR with reason %s", &logs, tc.want)
			}
			if strings.Contains(logs.String(), "PRIVATE_") || strings.Contains(logs.String(), "private wrapper") {
				t.Fatalf("unsafe report: %s", &logs)
			}
		})
	}
}
