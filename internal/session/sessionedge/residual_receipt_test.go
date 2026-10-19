package sessionedge

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestQueueRetainsDistinctResidualOutcome(t *testing.T) {
	for _, residual := range []error{
		diagnostic.NewTerminal(session.DTLS, "timeout", session.ErrUncertain),
		session.ErrBusy,
		errors.Join(session.ErrTimeout, session.ErrReleaseAbandoned),
	} {
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		q := &eventQueue{}
		primary := q.failed(session.ErrTimeout, session.DTLS)
		diagnostic.Log(logger, "layer_failed", "", primary)
		diagnostic.Log(logger, "layer_failed", "", q.failed(residual, session.DTLS))
		if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 2 {
			t.Fatalf("distinct residual reports=%d want 2: %s", n, &logs)
		}
		got := q.failed(residual, session.DTLS)
		for _, sentinel := range []error{session.ErrUncertain, session.ErrBusy, session.ErrReleaseAbandoned} {
			if errors.Is(residual, sentinel) && !errors.Is(got, sentinel) {
				t.Fatalf("residual policy/release metadata lost: %v", got)
			}
		}
		if !errors.Is(residual, session.ErrReleaseAbandoned) &&
			!errors.Is(terminalPriority(primary, got), policy(residual)) {
			t.Fatal("stronger residual policy lost")
		}
	}
}
