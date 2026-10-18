package p2ptunnel

import (
	"bytes"
	"testing"
	"time"
)

// Future reads throttle at about 3.6 MiB of device RDT backlog; PAUSE cannot
// retract it. Reserve a rounded 4 MiB backlog plus
// 1 MiB staging and one final read after a 4 MiB threshold is crossed.
func TestMultiMiBPauseHeadroom(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 4 << 20
	d := activeDriver(t, cfg, false)
	bulk, rpc := d.open(445), d.open(4700)
	volume := cfg.HighWater + 1 + (5 << 20) + MaxData
	for n := volume; n > 0; {
		size := min(n, MaxData)
		n -= size
		d.frame(Frame{DataCommand, bulk.Channel, bytes.Repeat([]byte{0xba}, size)})
	}
	if !d.m.Channel(bulk.Channel).Active || d.m.Channel(bulk.Channel).Queued != volume || !d.m.Channel(bulk.Channel).SentPause {
		t.Fatalf("multi-MiB threshold lost already-admitted burst: %+v", d.m.Channel(bulk.Channel))
	}
	if !d.m.Channel(rpc.Channel).Active || d.m.Channel(rpc.Channel).SentPause {
		t.Fatal("burst disturbed empty RPC")
	}
	if cfg.ChannelBytes != 16<<20 || cfg.SessionBytes != 64<<20 {
		t.Fatalf("default ceilings: channel=%d aggregate=%d", cfg.ChannelBytes, cfg.SessionBytes)
	}
}

// These fixtures exercise the 5+3 MiB aggregate boundary.
func legacyCapacityConfig() Config {
	cfg := DefaultConfig()
	cfg.ChannelBytes, cfg.SessionBytes, cfg.HighWater = 6<<20, 8<<20, 64<<10
	return cfg
}

// Exercise the enlarged default aggregate with a recently progressing largest
// holder and three stalled holders. Stalled precedence must still win.
func TestMultiMiBAggregateReclaimsStalledHolder(t *testing.T) {
	cfg := DefaultConfig()
	d := activeDriver(t, cfg, false)
	holders := make([]Action, 4)
	var first Action
	for i := range holders {
		holders[i] = d.open(445)
		for n := 0; n < cfg.ChannelBytes; n += MaxData {
			out := d.frame(Frame{DataCommand, holders[i].Channel, bytes.Repeat([]byte{0xba}, MaxData)})
			if i == 0 && n == 0 {
				first = mustAction(t, out, WriteLocal)
			}
		}
	}
	d.now = d.now.Add(2 * time.Second)
	d.step(local(Event{Kind: LocalWritten, Count: 1}, first))
	d.frame(Frame{DataCommand, holders[0].Channel, []byte{0xba}})
	if d.m.Usage().RXBytes != cfg.SessionBytes {
		t.Fatal("default aggregate not saturated")
	}
	rpc := d.open(4700)
	out := d.frame(Frame{DataCommand, rpc.Channel, []byte("rpc")})
	victim := mustAction(t, out, CloseLocal)
	if victim.Channel != holders[1].Channel || !d.m.Channel(holders[0].Channel).Active || d.m.Channel(holders[1].Channel).Active {
		t.Fatalf("new aggregate violated stalled precedence: victim=%d", victim.Channel)
	}
	w := mustAction(t, out, WriteLocal)
	if w.Channel != rpc.Channel || string(w.Data) != "rpc" {
		t.Fatal("reclamation lost RPC")
	}
}

// The full three-seed sweep favors 4 MiB for deadline margin and avoiding fast
// flow churn. Exercise that selected default with independent literal controls.
func TestDefaultPauseThresholdSelectedBySweep(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	ch := d.open(445)
	wire := observeFlow(d)
	d.frame(Frame{DataCommand, ch.Channel, []byte{1}})
	wire.Reset()
	d.step(Event{Kind: ReceiveQueueSize, Count: 3 << 20})
	if len(wire.Bytes()) != 0 || d.m.Channel(ch.Channel).SentPause {
		t.Fatalf("default paused below selected 4 MiB: %x", wire.Bytes())
	}
	d.step(Event{Kind: ReceiveQueueSize, Count: 4 << 20})
	if !bytes.Equal(wire.Bytes(), []byte{5, ch.Channel, 0, 0}) {
		t.Fatalf("default above 4 MiB=%x", wire.Bytes())
	}
}
