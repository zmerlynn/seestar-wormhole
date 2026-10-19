package proxy

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// A filled native maximum window can unlock in one packet. Draining it must
// coalesce redundant read demands instead of failing the shared owner inbox.
func TestCapacityComposedWindowDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 256), done: make(chan struct{}), cancel: cancel}
	x := &stack{ctx: ctx, cancel: cancel, f: &factory{s: s}, streams: make(map[uint64]*stream), actions: make(chan iotc.Action, 1), secureTX: make(chan []byte, 16), timerUpdates: make(chan time.Duration, 1)}
	for i := range x.handles {
		x.handles[i] = &layerHandle{notices: make(chan session.Event, 1)}
	}
	for i := range x.phases {
		x.phases[i] = make(chan error, 1)
	}
	cfg := runtimeRDTConfig()
	x.reliable, _ = rdt.New(cfg)
	x.reliable.Step(rdt.Event{Kind: rdt.Start}, 0)
	x.reliable.Step(rdt.Event{Kind: rdt.Receive, Packet: rdt.Packet{Type: rdt.Heartbeat, Version: 5, ID: 2}}, 0)
	x.reliable.Step(rdt.Event{Kind: rdt.Receive, Packet: rdt.Packet{Type: rdt.HeartbeatACK, Version: 5, ID: 1}}, 0)
	x.tunnel, _ = p2ptunnel.New(p2ptunnel.DefaultConfig())
	x.tunnel.Step(p2ptunnel.Event{Kind: p2ptunnel.Start}, 0)
	x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{8, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}}}, 0)
	c := newStream(s, 1, 445)
	s.clients[1] = c
	if err := (&layerHandle{x: x}).TryOpen(1); err != nil {
		t.Fatal(err)
	}
	frame := append([]byte{2, 1, 0, 32}, bytes.Repeat([]byte{0x17}, 8192)...)
	// Fixture envelope is independent of the tunnel encoder. Segmentation crosses
	// frame boundaries, as ordinary 1280-byte RDT packets do.
	stream := bytes.Repeat(frame, 1016)
	const packets = 6501 // native 6500 ceiling plus documented append allowance
	for i := 1; i < packets; i++ {
		p := rdt.Packet{Type: rdt.Data, Version: 5, ID: 1, Sequence: rdt.Sequence{Low: uint32(i)}, Payload: stream[i*1280 : (i+1)*1280]}
		x.drive(layerEvent{r: &rdt.Event{Kind: rdt.Receive, Packet: p}}, 0)
	}
	if x.reliable.Usage().RXPackets != packets-1 {
		t.Fatal("runtime bound cannot hold device window")
	}
	p := rdt.Packet{Type: rdt.Data, Version: 5, ID: 1, Payload: stream[:1280]}
	x.drive(layerEvent{r: &rdt.Event{Kind: rdt.Receive, Packet: p}}, 0)
	for turn := 0; len(x.queue) > 0 && !x.failed && turn < 100; turn++ {
		x.drive(layerEvent{}, core.Instant(turn+1))
	}
	if x.failed || ctx.Err() != nil || x.reliable.Usage().RXBytes != 0 || len(x.queue) != 0 {
		t.Fatalf("window drain killed/stalled session: failed=%v RX=%+v queued=%d", x.failed, x.reliable.Usage(), len(x.queue))
	}
	// This reader never completes writes. Exceeding the documented finite channel
	// envelope may close it, but neither RDT nor the session may fail.
	t.Logf("drained %d packets; session live, tunnel=%+v", packets, x.tunnel.Usage())
}
