package proxy

import (
	"context"
	"crypto/sha256"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestCapacityComposedProgressingReconnects(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		s, stacks, cloud := workloadSessionLegacyCapacity(t, ctx, func(p *simulatedWire) {
			p.bulkBurst = true
			p.responseBytes = (1 << 20) - 65
		})
		defer s.Close()
		x := <-stacks
		rpc, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer rpc.Close()
		pollCtx, stopPolling := context.WithCancel(ctx)
		pollDone := make(chan capacityPollResult, 1)
		go capacityPollRPC(pollCtx, rpc, time.Second, time.Second, pollDone)
		joined := false
		defer func() {
			stopPolling()
			rpc.Close()
			if !joined {
				<-pollDone
			}
		}()
		// Literal response oracle independent of the simulated peer builder.
		line := []byte(`{"jsonrpc":"2.0","id":99,"result":{"state":"idle","padding":"` + strings.Repeat("s", (1<<20)-65) + `"}}` + "\n")
		if len(line) != 1<<20 {
			t.Fatalf("literal response bytes=%d", len(line))
		}
		ref := sha256.New()
		for i := 0; i < 5; i++ {
			ref.Write(line)
		}
		var want [32]byte
		copy(want[:], ref.Sum(nil))
		started := time.Now()
		for round := 0; round < 8; round++ {
			func() {
				stuck, err := s.DialPort(ctx, 445)
				if err != nil {
					t.Fatal(err)
				}
				defer stuck.Close()
				if _, err := stuck.Write([]byte(strings.Repeat(rpcLine(99, 0), 3))); err != nil {
					t.Fatal(err)
				}
				until := time.Now().Add(10 * time.Second)
				for capacityTunnelSnapshot(t, x).RXBytes != 3<<20 {
					if time.Now().After(until) {
						t.Fatal("stalled backlog not ready")
					}
					time.Sleep(time.Millisecond) // synctest virtual time
				}
				healthy, err := s.DialPort(ctx, 445)
				if err != nil {
					t.Fatal(err)
				}
				defer healthy.Close()
				if _, err := healthy.Write([]byte(strings.Repeat(rpcLine(99, 0), 5))); err != nil {
					t.Fatal(err)
				}
				type received struct {
					n      int64
					err    error
					digest [32]byte
				}
				readDone := make(chan received, 1)
				readJoined := false
				go func() {
					h := sha256.New()
					n, err := io.CopyN(h, &capacityReader{reader: healthy, rate: 64 << 10}, 5<<20)
					var digest [32]byte
					copy(digest[:], h.Sum(nil))
					readDone <- received{n, err, digest}
				}()
				defer func() {
					healthy.Close()
					if !readJoined {
						<-readDone
					}
				}()
				time.Sleep(time.Second)
				// A delayed finite sibling burst exceeds the space freed by the
				// progressing reader. Both existing queues are within 6MiB.
				late, err := s.DialPort(ctx, 445)
				if err != nil {
					t.Fatal(err)
				}
				defer late.Close()
				if _, err := late.Write([]byte(rpcLine(99, 0))); err != nil {
					t.Fatal(err)
				}
				time.Sleep(time.Second)
				select {
				case <-stuck.(*stream).done:
				default:
					t.Fatalf("round %d: stalled holder survived pressure", round)
				}
				result := <-readDone
				readJoined = true
				if result.err != nil || result.n != 5<<20 || result.digest != want {
					t.Fatalf("round %d: progressing reconnect delivered=%d SHA256=%x want=%x err=%v", round, result.n, result.digest, want, result.err)
				}
				inspectStack(t, x)
				t.Logf("round=%d full bytes=%d SHA256=%x", round, result.n, result.digest)
			}()
			// Finish channel retirement before replacing its stalled pressure.
			synctest.Wait()
			if u := capacityTunnelSnapshot(t, x); u.RXBytes != 0 {
				t.Fatalf("round %d: retained=%+v", round, u)
			}
		}
		stopPolling()
		polls := <-pollDone
		joined = true
		if polls.err != nil || polls.count < 638 || polls.worst > time.Second || cloud.allocs.Load() != 1 {
			t.Fatalf("RPC calls=%d worst=%s err=%v allocations=%d", polls.count, polls.worst, polls.err, cloud.allocs.Load())
		}
		t.Logf("eight progressing reconnects completed, RPC=%d worst=%s elapsed=%s allocations=%d", polls.count, polls.worst, time.Since(started), cloud.allocs.Load())
	})
}
