package sessionedge

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"math/rand/v2"
	"testing"
	"time"
)

type drivenFactory struct {
	Factory
	work chan func(core.Instant)
}

func (f drivenFactory) Work() <-chan func(core.Instant) { return f.work }

func TestOwnerExecutesLayerWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := session.DefaultConfig()
	cfg.Idle = time.Second
	f := drivenFactory{work: make(chan func(core.Instant), 1)}
	called := make(chan core.Instant, 1)
	f.work <- func(now core.Instant) { called <- now; cancel() }
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg, rand.NewPCG(1, 2), f, nil, make(ChannelSink, 1)) }()
	select {
	case now := <-called:
		if now < 0 {
			t.Fatal("negative owner time")
		}
	case <-time.After(time.Second):
		t.Fatal("owner did not execute layer work")
	}
	<-done
}
