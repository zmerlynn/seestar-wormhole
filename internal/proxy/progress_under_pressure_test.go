package proxy

import (
	"context"
	"crypto/sha256"
	"io"
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestComposedProgressingReaderSurvivesPressure(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s, stacks, cloud := workloadSessionLegacyCapacity(t, ctx, func(p *simulatedWire) { p.bulkBurst = true; p.responseBytes = (1 << 20) - 65 })
		defer s.Close()
		x := <-stacks
		healthy, err := s.DialPort(ctx, 445)
		if err != nil {
			t.Fatal(err)
		}
		defer healthy.Close()
		rpc, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer rpc.Close()
		for i := 0; i < 4; i++ {
			if _, err := healthy.Write([]byte(rpcLine(99, 0))); err != nil {
				t.Fatal(err)
			}
		}
		type received struct {
			n      int64
			err    error
			digest [32]byte
		}
		readDone := make(chan received, 1)
		joined := false
		started := time.Now()
		go func() {
			h := sha256.New()
			n, err := io.CopyN(h, &capacityReader{reader: healthy, rate: 64 << 10}, 4<<20)
			var digest [32]byte
			copy(digest[:], h.Sum(nil))
			readDone <- received{n, err, digest}
		}()
		defer func() {
			healthy.Close()
			if !joined {
				<-readDone
			}
		}()
		time.Sleep(time.Second) // Virtual clock; reader has consumed at normal 64KiB/s.
		t.Logf("before pressure: elapsed=%s usage=%+v", time.Since(started), capacityTunnelSnapshot(t, x))
		var stuck []net.Conn
		for i, count := range []int{2, 2, 1} {
			c, err := s.DialPort(ctx, 445)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			stuck = append(stuck, c)
			requests := ""
			for j := 0; j < count; j++ {
				requests += rpcLine(99, 0)
			}
			if _, err := c.Write([]byte(requests)); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Second)
			t.Logf("after stuck reader %d: %+v", i, capacityTunnelSnapshot(t, x))
		}
		if _, err := capacityRPC(rpc, 1, 0, time.Second); err != nil {
			t.Fatal(err)
		}
		active := 0
		for _, c := range stuck {
			select {
			case <-c.(*stream).done:
			default:
				active++
			}
		}
		result := <-readDone
		joined = true
		t.Logf("normal-rate transfer delivered=%d elapsed=%s err=%v stuck-active=%d cloud allocations=%d", result.n, time.Since(started), result.err, active, cloud.allocs.Load())
		if result.err != nil {
			t.Fatalf("progressing 64KiB/s transfer terminated with %d stuck readers still active: %v", active, result.err)
		}
		// A repaired policy must permit full content delivery, not only survive
		// the first pressure event. This literal response is independent of the
		// peer's response builder and the tunnel encoder.
		line := []byte(`{"jsonrpc":"2.0","id":99,"result":{"state":"idle","padding":"` + strings.Repeat("s", (1<<20)-65) + `"}}` + "\n")
		if len(line) != 1<<20 {
			t.Fatalf("literal response length=%d want=%d", len(line), 1<<20)
		}
		reference := sha256.New()
		for i := 0; i < 4; i++ {
			reference.Write(line)
		}
		var want [32]byte
		copy(want[:], reference.Sum(nil))
		if result.n != 4<<20 || result.digest != want {
			t.Fatalf("full transfer bytes/hash: got=%d/%x want=%d/%x", result.n, result.digest, 4<<20, want)
		}
		if cloud.allocs.Load() != 1 {
			t.Fatal("aggregate pressure replaced session")
		}

	})
}
