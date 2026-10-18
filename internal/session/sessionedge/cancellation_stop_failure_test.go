package sessionedge

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"testing"
	"testing/synctest"
)

func TestCancellationReportsStopFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		f.stuckStop = true
		ctx, cancel := context.WithCancel(context.Background())
		in := make(chan session.Event, 1)
		out := make(ChannelSink, 16)
		done := make(chan error, 1)
		go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, f, in, out) }()
		in <- session.Event{Kind: session.Client, Client: 1}
		outcome(t, out, session.Admit)
		cancel()
		err := <-done
		if !errors.Is(err, session.ErrStop) {
			t.Fatalf("Run error=%v, want stop failure included; stop calls=%d", err, f.stopped.Load())
		}
	})
}
