package proxy

import (
	"context"
	"errors"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Independent policy expectations: every observed malformed outcome outranks
// every retryable cause, regardless of which operation contributed it.
func TestCloudOutcomeCannotLowerMalformed(t *testing.T) {
	for _, retry := range []error{cloud.ErrTransport, cloud.ErrOffline, context.Canceled, context.DeadlineExceeded} {
		for _, causes := range [][]error{{cloud.ErrInvalid, retry}, {retry, cloud.ErrInvalid}} {
			if got := cloudOutcome(errors.Join(causes...)); got != session.ErrUncertain {
				t.Errorf("outcome of %v = %v, want uncertain", causes, got)
			}
		}
	}
}
