package proxy

import (
	"context"
	"net"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// The review's real-SMB probe gates loopback TCP through the production owner;
// protocol adapters do not carry SMB bytes. No device is contacted.
func TestPullRealSMBTransportRecovery(t *testing.T) {
	addr := os.Getenv("SEESTAR_TEST_SAMBA_ADDR")
	if addr == "" {
		t.Skip("requires container loopback Samba fixture")
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()
		calls := 0
		f := &pullBusyFactory{}
		f.allocate = func(context.Context) (sessionedge.Lease, error) { calls++; return f, nil }
		s := pullBusySession(t, ctx, 90*time.Second, f)
		open := func() error {
			ctx, src, err := pull.OpenSMB(pull.Configure(ctx, pull.DefaultConfig()), func(ctx context.Context, _, _ string) (net.Conn, error) {
				admitted, err := s.DialPort(ctx, 445)
				if err != nil {
					return nil, err
				}
				admitted.Close()
				return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			})
			if err != nil {
				return err
			}
			entries, err := src.List(ctx, "")
			shutdownErr := src.Features().Shutdown(ctx)
			if err != nil {
				return err
			}
			if shutdownErr != nil {
				return shutdownErr
			}
			if len(entries) == 0 {
				t.Error("empty SMB inventory")
			}
			return nil
		}
		if err := open(); err != nil {
			t.Fatal("initial SMB establishment:", err)
		}
		channels := <-f.channels
		<-time.After(10 * time.Minute)
		channels.events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
		synctest.Wait()
		err := open()
		closeErr := s.Close()
		t.Logf("real SMB reconnect at 10m with 90s busy-wait: err=%v allocations=%d stops=%d work_ctx=%v owner_close=%v", err, calls, f.stops.Load(), ctx.Err(), closeErr)
		if err != nil || calls != 2 || f.stops.Load() != 2 || ctx.Err() != nil || closeErr != nil {
			t.Errorf("available-device SMB transport recovery failed: err=%v allocations=%d stops=%d", err, calls, f.stops.Load())
		}
	})
}
