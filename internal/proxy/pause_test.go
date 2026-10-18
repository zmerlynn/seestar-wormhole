package proxy

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
)

// Verify the internal session option reaches the actual running tunnel core.
func TestPauseThresholdRuntime(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s, stacks, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{PauseThreshold: 4 << 20})
		defer s.Close()
		c, err := s.DialPort(ctx, 445)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		x := <-stacks
		done := make(chan struct{})
		if !x.post(func(now core.Instant) {
			// One retained byte plus shared RDT pressure exercises the configured B.
			x.tunnel.Step(p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{2, 1, 1, 0, 'B'}}, now)
			x.tunnel.Step(p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: 4<<20 - 1}, now)
			if x.tunnel.Channel(1).SentPause {
				t.Error("configured threshold paused at equality")
			}
			x.tunnel.Step(p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: 4 << 20}, now)
			if !x.tunnel.Channel(1).SentPause {
				t.Error("configured threshold did not pause above high water")
			}
			close(done)
		}) {
			t.Fatal("inspection canceled")
		}
		<-done
	})
}
