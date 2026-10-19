package proxy

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"testing"
	"time"
)

func TestCanceledVerifiedOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 256), done: make(chan struct{}), cancel: cancel}
	x := &stack{ctx: ctx, cancel: cancel, f: &factory{s: s}, streams: make(map[uint64]*stream), timerUpdates: make(chan time.Duration, 1)}
	for i := range x.phases {
		x.phases[i] = make(chan error, 1)
	}
	m, err := p2ptunnel.New(p2ptunnel.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	x.tunnel = m
	m.Step(p2ptunnel.Event{Kind: p2ptunnel.Start}, 0)
	// Literal PASS with port verification enabled.
	x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{8, 0, 8, 0, 0, 0, 0, 0, 1, 0, 0, 0}}}, 0)
	c := newStream(s, 1, 4700)
	s.clients[1] = c
	h := &layerHandle{x: x}
	if err = h.TryOpen(1); err != nil {
		t.Fatal(err)
	}
	c.Close()
	h.TryClose(1)
	// The caller canceled; a valid late verification reply must not orphan a channel.
	x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{13, 0, 6, 0, 1, 0, 0x5c, 0x12, 1, 0}}}, core.Instant(time.Millisecond))
	if n := m.Usage().Channels; n != 0 {
		t.Fatalf("canceled DialPort left %d active tunnel channel(s), registered streams=%d", n, len(x.streams))
	}
}
