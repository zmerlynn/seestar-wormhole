package proxy

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
)

func TestFairPauseComposedBulkRPC(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		var rpcPauses, bulkPauses atomic.Int32
		// Observe every control delivered through real IOTC/DTLS/RDT, including
		// transient PAUSE followed by RESUME between owner snapshots.
		var decoder p2ptunnel.Decoder
		s, stacks, cloud := workloadSession(t, ctx, func(p *simulatedWire) {
			p.responseBytes = 1 << 20
			p.bulkBurst = true
			p.rdtGapAfter, p.rdtGapPackets = 150, 700
			p.tunnelInput = func(e p2ptunnel.Event, m *p2ptunnel.Machine) {
				if e.Kind != p2ptunnel.Stream || m.Status() != p2ptunnel.Active {
					return
				}
				for data := e.Data; len(data) > 0; {
					n, f, err := decoder.Feed(data, p2ptunnel.Established)
					if err != nil {
						t.Errorf("control observation: %v", err)
						return
					}
					data = data[n:]
					if f != nil && f.Command == p2ptunnel.PauseCommand {
						switch f.Channel {
						case 1:
							bulkPauses.Add(1)
						case 2:
							rpcPauses.Add(1)
						}
					}
				}
			}
		}, Config{PauseThreshold: 64 << 10})
		defer s.Close()
		bulk, err := s.DialPort(ctx, 445)
		if err != nil {
			t.Fatal(err)
		}
		defer bulk.Close()
		rpc, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer rpc.Close()
		x := <-stacks
		for i := 0; i < 4; i++ {
			if _, err := bulk.Write([]byte(rpcLine(99, 0))); err != nil {
				t.Fatal(err)
			}
		}
		type snapshot struct {
			shared    int
			bulk, rpc p2ptunnel.ChannelState
		}
		peak := 0
		until := time.Now().Add(10 * time.Second)
		for {
			done := make(chan snapshot, 1)
			if !x.post(func(core.Instant) {
				done <- snapshot{x.reliable.Usage().RXBytes, x.tunnel.Channel(1), x.tunnel.Channel(2)}
			}) {
				t.Fatal("inspection canceled")
			}
			v := <-done
			peak = max(peak, v.shared)
			if v.rpc.Queued == 0 && v.rpc.SentPause {
				t.Errorf("empty RPC paused with shared=%d bulk=%d", v.shared, v.bulk.Queued)
			}
			if v.shared > 65536 && v.bulk.Queued > 0 && v.rpc.Queued == 0 {
				break
			}
			if time.Now().After(until) {
				t.Fatalf("did not exercise shared backlog over 65536 with retained bulk: peak=%d snapshot=%+v", peak, v)
			}
			time.Sleep(time.Millisecond) // synctest virtual clock
		}
		for id := 1; id <= 12; id++ {
			if _, err := capacityRPC(rpc, id, 0, time.Second); err != nil {
				t.Fatalf("RPC %d under bulk pressure: %v", id, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		inspectStack(t, x)
		if got := rpcPauses.Load(); got != 0 {
			t.Fatalf("RPC PAUSE controls=%d want 0 (including transient controls)", got)
		}
		if bulkPauses.Load() == 0 || cloud.allocs.Load() != 1 {
			t.Fatalf("bulk PAUSE=%d allocations=%d want pressure on one allocation", bulkPauses.Load(), cloud.allocs.Load())
		}
		t.Logf("shared RX peak observed=%d, bulk PAUSE=%d, RPC PAUSE=0, 12 replies delivered", peak, bulkPauses.Load())
	})
}
