package sessionedge

import (
	"context"
	"errors"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestCancellationErrorNormalization(t *testing.T) {
	for _, cause := range []error{context.Canceled, errors.Join(session.ErrTransport, context.Canceled)} {
		got := constructorFailure(cause, session.DTLS)
		if terminalPriority(nil, got) != nil {
			t.Fatalf("cancellation-only terminal retained: %v", got)
		}
	}
	for _, stronger := range []error{session.ErrBusy, session.ErrUncertain, session.ErrStop} {
		got := constructorFailure(errors.Join(session.ErrTimeout, context.Canceled, stronger), session.DTLS)
		if p, r := diagnostic.Fields(got, ""); p != "dtls" || r != "timeout" {
			t.Fatalf("received timeout attribution=%s/%s want dtls/timeout", p, r)
		}
		if !errors.Is(got, stronger) {
			t.Fatalf("policy/release metadata lost: %v", got)
		}
	}
}
