package proxy

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// A literal ten-second loss window starts only after an established RPC.
// synctest.Wait barriers ensure the new dial is submitted inside that window.
func TestOutagePendingOpen(t *testing.T) {
	for _, verify := range []bool{false, true} {
		name := "without-verification"
		if verify {
			name = "with-verification"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var offline atomic.Bool
				s, stacks, cloud := workloadSession(t, ctx, func(p *simulatedWire) {
					p.verify = verify
					p.outage = offline.Load
				})
				defer s.Close()
				established, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatal(err)
				}
				defer established.Close()
				x := <-stacks
				if err := exchange(established.(*stream), 710, 0); err != nil {
					t.Fatal(err)
				}
				offline.Store(true)
				type result struct {
					c   net.Conn
					err error
				}
				opened := make(chan result, 1)
				go func() { c, err := s.DialPort(ctx, 4700); opened <- result{c, err} }()
				synctest.Wait()
				rpc := make(chan error, 1)
				go func() { rpc <- exchange(established.(*stream), 711, 0) }()
				synctest.Wait()
				<-time.After(6 * time.Second)
				synctest.Wait()
				checked := make(chan struct{})
				x.post(func(core.Instant) {
					t.Logf("loss+6s: failed=%v tunnel=%v usage=%+v RDT=%+v streams=%d retries=%d", x.failed, x.tunnel.Status(), x.tunnel.Usage(), x.reliable.Usage(), len(x.streams), len(x.pending))
					close(checked)
				})
				<-checked
				<-time.After(4 * time.Second)
				offline.Store(false)
				synctest.Wait()
				r := <-opened
				if err := <-rpc; err != nil {
					t.Fatalf("established RPC did not survive: %v", err)
				}
				if err := exchange(established.(*stream), 712, 0); err != nil {
					t.Fatal(err)
				}

				t.Logf("after recovery: allocations=%d stops=%d", cloud.allocs.Load(), cloud.stops.Load())

				if r.err != nil {
					t.Fatalf("new open rejected during surviving ten-second outage: %v", r.err)
				}
				defer r.c.Close()
				if err := exchange(r.c.(*stream), 713, 0); err != nil {
					t.Fatal(err)
				}
				inspectStack(t, x)
			})
		})
	}
}
