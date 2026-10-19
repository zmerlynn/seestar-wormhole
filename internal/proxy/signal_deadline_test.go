//go:build unix

package proxy

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Stop is entered after its deadline started. Requiring a full configured
// timeout from this later point would fail even with correct deadline handling.
func TestOwedSignalStopObservesActualDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()
		<-time.After(20 * time.Millisecond)
		f := new(owedSignalFactory)
		err := f.Stop(ctx)
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, session.ErrReleaseAbandoned) {
			t.Errorf("stop result=%v, want deadline and abandonment", err)
		}
		if f.stops != 1 || f.elapsed > time.Second {
			t.Errorf("stops/elapsed=%d/%v, want 1 and <=1s", f.stops, f.elapsed)
		}
	})
}
