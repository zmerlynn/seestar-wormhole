package p2ptunnel

import (
	"bytes"
	"testing"
)

// observeFlow retains actual emitted bytes; literal expectations below never
// decode and rebuild a control through the production codec.
func observeFlow(d *driver) *bytes.Buffer {
	wire := new(bytes.Buffer)
	d.observeSend = func(data []byte) {
		if len(data) > 0 && data[0] >= 5 && data[0] <= 7 {
			wire.Write(data)
		}
	}
	return wire
}

// Literal controls/envelopes exercise frame and flow-control behavior. The
// 65,537 shared-byte case reproduces the HOL boundary; empty channels must not
// inherit another channel's pressure.
func TestFairPauseLiteral65537(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 65536 // Retained literal HOL fixture, independent of runtime default.
	d := activeDriver(t, cfg, false)
	wire := observeFlow(d)
	bulk, rpc := d.open(445), d.open(4700)
	wire.Reset()
	d.step(Event{Kind: ReceiveQueueSize, Count: 65537})
	if got := wire.Bytes(); len(got) != 0 {
		t.Fatalf("empty-channel flow wire=%x want none", got)
	}
	// One retained bulk byte gives its channel the shared pressure; the idle
	// RPC remains empty. Use independent literal DATA, not the encoder.
	w := mustAction(t, d.step(Event{Kind: Stream, Data: []byte{2, 1, 1, 0, 'B'}}), WriteLocal)
	if got := wire.Bytes(); !bytes.Equal(got, []byte{5, 1, 0, 0}) {
		t.Fatalf("flow wire=%x want 05010000", got)
	}
	if s := d.m.Channel(rpc.Channel); s.Queued != 0 || s.SentPause {
		t.Fatalf("empty RPC state=%+v want unpaused", s)
	}
	wire.Reset()
	// Own byte + shared count equals low water: no transition at equality.
	d.step(Event{Kind: ReceiveQueueSize, Count: 52427})
	if !d.m.Channel(bulk.Channel).SentPause || len(wire.Bytes()) != 0 {
		t.Fatal("low-water equality resumed bulk")
	}
	d.step(Event{Kind: ReceiveQueueSize, Count: 52426})
	if got := wire.Bytes(); !bytes.Equal(got, []byte{6, 1, 0, 0}) {
		t.Fatalf("resume wire=%x want 06010000", got)
	}
	d.step(Event{Kind: ReceiveQueueSize, Count: 65537})
	wire.Reset()
	d.step(local(Event{Kind: LocalWritten, Count: 1}, w))
	if s := d.m.Channel(bulk.Channel); s.Queued != 0 || s.SentPause {
		t.Fatalf("drained holder state=%+v want empty and resumed despite shared backlog", s)
	}
	if got := wire.Bytes(); !bytes.Equal(got, []byte{6, 1, 0, 0}) {
		t.Fatalf("empty-holder wire=%x want 06010000", got)
	}
}

func TestFairPauseWeightedHolders(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 100
	d := activeDriver(t, cfg, false)
	wire := observeFlow(d)
	a, b, rpc := d.open(445), d.open(445), d.open(4700)
	w := mustAction(t, d.frame(Frame{DataCommand, a.Channel, bytes.Repeat([]byte{'A'}, 75)}), WriteLocal)
	d.frame(Frame{DataCommand, b.Channel, bytes.Repeat([]byte{'B'}, 25)})
	wire.Reset()
	d.step(Event{Kind: ReceiveQueueSize, Count: 100})
	// Q(A)=150 and Q(B)=50. Charging the shared count once to each holder
	// would pause B too. Charging nothing would fail to pause A.
	if got := wire.Bytes(); !bytes.Equal(got, []byte{5, 1, 0, 0}) {
		t.Fatalf("weighted pause wire=%x want 05010000", got)
	}
	wire.Reset()
	d.step(local(Event{Kind: LocalWritten, Count: 75}, w))
	// A draining changes B's share even though B received no event.
	if got := wire.Bytes(); !bytes.Equal(got, []byte{5, 2, 0, 0, 6, 1, 0, 0}) {
		t.Fatalf("redistributed wire=%x want 0502000006010000", got)
	}
	if d.m.Channel(rpc.Channel).SentPause {
		t.Fatal("idle RPC inherited redistributed pressure")
	}
	// Closing B must release all retained storage without disturbing RPC.
	d.step(local(Event{Kind: LocalClose}, b))
	if d.m.Usage().RXBytes != 0 || !d.m.Channel(rpc.Channel).Active || d.m.Channel(rpc.Channel).SentPause {
		t.Fatalf("close accounting=%+v RPC=%+v", d.m.Usage(), d.m.Channel(rpc.Channel))
	}
}

func TestFairPauseHighWaterAndRounding(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 100
	d := activeDriver(t, cfg, false)
	wire := observeFlow(d)
	a, b := d.open(445), d.open(445)
	d.frame(Frame{DataCommand, a.Channel, bytes.Repeat([]byte{'A'}, 75)})
	d.frame(Frame{DataCommand, b.Channel, bytes.Repeat([]byte{'B'}, 25)})
	wire.Reset()
	// floor(34*75/100)+75=100: strict high-water equality holds.
	d.step(Event{Kind: ReceiveQueueSize, Count: 34})
	if got := wire.Bytes(); len(got) != 0 {
		t.Fatalf("high-water equality wire=%x want none", got)
	}
	d.step(Event{Kind: ReceiveQueueSize, Count: 35})
	if got := wire.Bytes(); !bytes.Equal(got, []byte{5, 1, 0, 0}) {
		t.Fatalf("above high-water wire=%x want 05010000", got)
	}
}

func TestFairPauseWakeUsesAttributedPressure(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 10125 // low water = 8100
	d := activeDriver(t, cfg, false)
	wire := observeFlow(d)
	a, b := d.open(445), d.open(445)
	wa := mustAction(t, d.frame(Frame{DataCommand, a.Channel, bytes.Repeat([]byte{'A'}, 4100)}), WriteLocal)
	wb := mustAction(t, d.frame(Frame{DataCommand, b.Channel, bytes.Repeat([]byte{'B'}, MaxData)}), WriteLocal)
	d.frame(Frame{DataCommand, b.Channel, bytes.Repeat([]byte{'B'}, MaxData)})
	d.step(Event{Kind: ReceiveQueueSize, Count: 40000})
	d.step(local(Event{Kind: LocalWritten, Count: MaxData}, wb))
	d.step(Event{Kind: ReceiveQueueSize, Count: 12017})
	wire.Reset()
	// Drained total=8197; Q(A)=4095+floor(12017*4095/12287)=8100.
	// Equality preserves PAUSE but must not grant a WAKE opportunity.
	next := mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 5}, wa)), WriteLocal)
	if !d.m.Channel(a.Channel).SentPause || len(wire.Bytes()) != 0 {
		t.Fatalf("low-water equality wire=%x paused=%v want paused without WAKE", wire.Bytes(), d.m.Channel(a.Channel).SentPause)
	}
	d.step(Event{Kind: ReceiveQueueSize, Count: 12300})
	d.step(local(Event{Kind: LocalWritten, Count: 1}, next))
	if got := wire.Bytes(); !bytes.Equal(got, []byte{7, 1, 0, 0}) || !d.m.Channel(a.Channel).SentPause {
		t.Fatalf("wake wire=%x paused=%v want 07010000 without clearing PAUSE", got, d.m.Channel(a.Channel).SentPause)
	}
}

func TestFairPauseReleaseRedistributes(t *testing.T) {
	for _, kind := range []EventKind{LocalClose, LocalWriteFailed} {
		t.Run(map[EventKind]string{LocalClose: "close", LocalWriteFailed: "write-failure"}[kind], func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.HighWater = 100
			d := activeDriver(t, cfg, false)
			wire := observeFlow(d)
			a, b := d.open(445), d.open(445)
			wa := mustAction(t, d.frame(Frame{DataCommand, a.Channel, bytes.Repeat([]byte{'A'}, 75)}), WriteLocal)
			d.frame(Frame{DataCommand, b.Channel, bytes.Repeat([]byte{'B'}, 25)})
			d.step(Event{Kind: ReceiveQueueSize, Count: 100})
			wire.Reset()
			d.step(local(Event{Kind: kind}, wa))
			if !d.m.Channel(b.Channel).SentPause || d.m.Usage().RXBytes != 25 {
				t.Fatalf("remaining holder=%+v usage=%+v want paused and 25 retained bytes", d.m.Channel(b.Channel), d.m.Usage())
			}
			if d.m.Channel(a.Channel).SentPause {
				t.Fatal("released backlog retained PAUSE")
			}
		})
	}
}
