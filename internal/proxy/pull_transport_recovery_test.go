package proxy

import (
	"context"
	"errors"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// Ported from the review's before/after probe. Every allocator call succeeds;
// the only injected failure is transport loss after an established session.
func TestPullTransportRecovery(t *testing.T) {
	for _, budget := range []time.Duration{0, 90 * time.Second} {
		for _, lostAt := range []time.Duration{20 * time.Second, 91 * time.Second, 10 * time.Minute} {
			t.Run(budget.String()+"/loss-"+lostAt.String(), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
					defer cancel()
					began := time.Now()
					attempts, admissions, backendDials := 0, 0, 0
					f := &pullBusyFactory{}
					f.allocate = func(context.Context) (sessionedge.Lease, error) { attempts++; return f, nil }
					s := pullBusySession(t, ctx, budget, f)
					conn, err := s.DialPort(ctx, 445)
					if err != nil {
						t.Fatal(err)
					}
					channels := <-f.channels
					<-time.After(lostAt)
					// Opening a channel proves the held session is still healthy at this time.
					healthy, err := s.DialPort(ctx, 445)
					if err != nil {
						t.Fatal(err)
					}
					healthy.Close()
					channels.events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
					synctest.Wait()
					conn.Close()
					_, _, err = pull.OpenSMB(pull.Configure(ctx, pull.DefaultConfig()), func(ctx context.Context, _, _ string) (net.Conn, error) {
						backendDials++
						c, err := s.DialPort(ctx, 445)
						if err != nil {
							return nil, err
						}
						admissions++
						c.Close()
						// Proves owner re-admission through all rclone retries, without SMB wire.
						return nil, net.ErrClosed
					})
					closeErr := s.Close()
					synctest.Wait()
					t.Logf("budget=%v loss=%v elapsed=%v allocs=%d backend_dials=%d admissions=%d err=%v stop=%d close=%d owner_close=%v work_ctx=%v", budget, lostAt, time.Since(began), attempts, backendDials, admissions, err, f.stops.Load(), f.closed.Load(), closeErr, ctx.Err())
					if admissions != 3 || attempts != 2 || !errors.Is(err, net.ErrClosed) || errors.Is(err, session.ErrBusy) || ctx.Err() != nil {
						t.Errorf("transport recovery regressed: admissions=%d allocs=%d busy=%t; want 3/2/false before work timeout", admissions, attempts, errors.Is(err, session.ErrBusy))
					}
					if f.stops.Load() != int32(attempts) || f.closed.Load() != 4*int32(attempts) || closeErr != nil {
						t.Errorf("unclean recovery release: stops=%d closes=%d attempts=%d err=%v", f.stops.Load(), f.closed.Load(), attempts, closeErr)
					}
				})
			})
		}
	}
}
