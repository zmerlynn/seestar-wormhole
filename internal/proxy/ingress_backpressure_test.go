package proxy

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"testing"
	"time"
)

func TestStalledWireIngressDoesNotKillSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x := &stack{ctx: ctx, cancel: cancel, actions: make(chan iotc.Action, 1), timerUpdates: make(chan time.Duration, 1)}
	for i := range x.handles {
		x.handles[i] = &layerHandle{notices: make(chan session.Event, 1)}
	}
	x.effects = []iotc.Action{{Kind: iotc.Send}, {Kind: iotc.Send}}
	for i := 0; i < 1024; i++ {
		x.drive(layerEvent{i: &iotc.Event{Kind: iotc.Wire, Data: make([]byte, 1388)}}, 0)
	}
	if x.failed || ctx.Err() != nil {
		t.Fatal("ordinary receive saturation killed session")
	}
	if len(x.queue) > 256 {
		t.Fatalf("unbounded receive queue: %d", len(x.queue))
	}
}

func TestStalledWireStillReleasesLocalOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 256), done: make(chan struct{}), cancel: cancel}
	x := &stack{ctx: ctx, cancel: cancel, f: &factory{s: s}, streams: make(map[uint64]*stream), actions: make(chan iotc.Action, 1), timerUpdates: make(chan time.Duration, 1)}
	for i := range x.phases {
		x.phases[i] = make(chan error, 1)
	}
	x.tunnel, _ = p2ptunnel.New(p2ptunnel.DefaultConfig())
	x.tunnel.Step(p2ptunnel.Event{Kind: p2ptunnel.Start}, 0)
	x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{8, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}}}, 0)
	c := newStream(s, 1, 4700)
	s.clients[1] = c
	h := &layerHandle{x: x}
	if err := h.TryOpen(1); err != nil {
		t.Fatal(err)
	}
	x.effects = []iotc.Action{{Kind: iotc.Send}, {Kind: iotc.Send}}
	h.TryClose(1)
	if x.tunnel.Usage().Channels != 0 {
		t.Fatal("stalled wire prevented local channel release")
	}
}
