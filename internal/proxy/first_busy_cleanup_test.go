package proxy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

func TestAmbientFirstBusyPartialLeaseCleanup(t *testing.T) {
	for _, stopErr := range []error{nil, session.ErrStop, session.ErrUncertain, session.ErrReleaseAbandoned} {
		t.Run(fmt.Sprint(stopErr), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				f := &busyFactory{stopError: stopErr, channels: make(chan *busyChannels, 8)}
				f.allocate = func(context.Context) (sessionedge.Lease, error) {
					if int(f.stops.Load()) != attempts {
						t.Errorf("replacement before previous stop: attempts/stops=%d/%d", attempts, f.stops.Load())
					}
					attempts++
					if attempts <= 3 {
						return f, session.ErrBusy
					}
					return f, nil
				}
				s, err := startWithRNG(t.Context(), Config{}, func(s *Session) sessionedge.Factory { f.s = s; return f }, zeroRetryRNG{})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				<-time.After(time.Minute)
				synctest.Wait()
				wantAttempts := 4
				if stopErr != nil {
					wantAttempts = 1
				}
				if attempts != wantAttempts || int(f.stops.Load()) != min(3, wantAttempts) {
					t.Fatalf("attempts/stops=%d/%d, want %d/%d", attempts, f.stops.Load(), wantAttempts, min(3, wantAttempts))
				}
				if stopErr == nil {
					c, err := s.DialPort(t.Context(), 445)
					if err != nil {
						t.Fatal(err)
					}
					c.Close()
				} else if _, err := s.DialPort(t.Context(), 445); !errors.Is(err, session.ErrStop) && !errors.Is(err, session.ErrUncertain) {
					t.Fatalf("blocked client=%v, want cleanup failure", err)
				}
				if err := s.Close(); (err != nil) != (stopErr != nil) {
					t.Fatalf("close=%v, cleanup failure=%v", err, stopErr)
				}
				if int(f.stops.Load()) != wantAttempts {
					t.Fatalf("stops=%d, want exactly one per allocation (%d)", f.stops.Load(), wantAttempts)
				}
				<-time.After(24 * time.Hour)
				synctest.Wait()
				if attempts != wantAttempts || int(f.stops.Load()) != wantAttempts {
					t.Fatal("work after shutdown")
				}
			})
		})
	}
}
