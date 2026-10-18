package proxy

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestPullDemandBusyPolicy(t *testing.T) {
	for _, budget := range []time.Duration{0, 5 * time.Second, 90 * time.Second} {
		t.Run(budget.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tr := &lazyBusyCloud{route: "precheck", ready: time.Now().Add(36 * time.Second)}
				s := lazyBusySession(t, context.Background(), Config{HoldOnDemand: true, BusyWait: budget}, tr)
				defer s.Close()
				synctest.Wait()
				if tr.attempts.Load() != 0 || tr.allocs.Load() != 0 {
					t.Fatal("pull acquired before identity binding")
				}
				began := time.Now()
				conn, err := s.DialPort(context.Background(), 4700)
				if budget < 36*time.Second {
					if !errors.Is(err, session.ErrBusy) {
						t.Fatalf("busy=%v", err)
					}
					if budget == 0 && tr.attempts.Load() != 1 {
						t.Fatalf("fail-fast attempts=%d", tr.attempts.Load())
					}
					if time.Since(began) > budget+time.Second {
						t.Fatalf("unbounded busy wait=%v", time.Since(began))
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					conn.Close()
					<-time.After(2 * time.Hour)
					synctest.Wait()
					if tr.allocs.Load() != 1 || tr.stops.Load() != 0 {
						t.Fatal("pull released between files")
					}
				}
			})
		})
	}
}
