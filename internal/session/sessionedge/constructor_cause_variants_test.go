package sessionedge

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"testing"
)

func TestConstructorJoinedCauseVariants(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		want  string
	}{
		{"native-known", errors.Join(diagnostic.NewTerminal(session.DTLS, "canceled", session.ErrTransport), session.ErrTimeout), "timeout"},
		{"wrapped-known", diagnostic.Wrap("dtls", "canceled", errors.Join(context.Canceled, session.ErrTimeout)), "timeout"},
		{"unknown", errors.Join(context.Canceled, errors.New("PRIVATE_COORDINATES_TIME_TOKEN_UID_PAYLOAD")), "operation_failed"},
		{"quiet", errors.Join(context.Canceled, session.ErrTransport), "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := constructorFailure(tc.cause, session.DTLS)
			if phase, reason := diagnostic.Fields(got, ""); phase != "dtls" || reason != tc.want {
				t.Errorf("normalized=%s/%s want dtls/%s", phase, reason, tc.want)
			}
			if tc.name == "quiet" && terminalPriority(nil, got) != nil {
				t.Fatal("operator cancel made noisy")
			}
		})
	}
}
