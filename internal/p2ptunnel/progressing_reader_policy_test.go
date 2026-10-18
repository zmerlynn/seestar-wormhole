package p2ptunnel

import (
	"bytes"
	"testing"
	"time"
)

// The progressing reader consumes at the accepted 64KiB/s. All DATA is
// finite prequeued traffic; no reader needs to ignore PAUSE or exceed 6MiB.
func TestProgressingReaderSurvivesAggregatePressure(t *testing.T) {
	d := activeDriver(t, legacyCapacityConfig(), false)
	healthy, stuck, rpc := d.open(445), d.open(445), d.open(4700)
	input := bytes.Repeat([]byte{0x71}, MaxData)
	first := Action{}
	for _, burst := range []struct {
		ch   Action
		size int
	}{{healthy, 5 << 20}, {stuck, 3 << 20}} {
		for i := 0; i < burst.size/MaxData; i++ {
			out := d.frame(Frame{DataCommand, burst.ch.Channel, input})
			if i == 0 && burst.ch.Channel == healthy.Channel {
				first = mustAction(t, out, WriteLocal)
			}
		}
	}
	w := first
	for i := 0; i < 8; i++ {
		d.now = d.now.Add(time.Second / 8)
		w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: MaxData}, w)), WriteLocal)
	}
	// Delayed tail of the same finite already-queued burst, replenishing only the
	// 64KiB just consumed. The live queue never exceeds its 5MiB initial size.
	for i := 0; i < 8; i++ {
		d.frame(Frame{DataCommand, healthy.Channel, input})
	}
	out := d.frame(Frame{DataCommand, rpc.Channel, []byte("rpc\n")})
	close := mustAction(t, out, CloseLocal)
	t.Logf("consumed=65536 in 1s; victim=%d healthy=%d stuck=%d stuck-active=%v usage=%+v", close.Channel, healthy.Channel, stuck.Channel, d.m.Channel(stuck.Channel).Active, d.m.Usage())
	if !d.m.Channel(healthy.Channel).Active || d.m.Channel(stuck.Channel).Active {
		t.Fatalf("aggregate pressure closed progressing transfer while stuck reader survived: close=%d", close.Channel)
	}
}

func TestReclamationThrashesProgressingReaders(t *testing.T) {
	d := activeDriver(t, legacyCapacityConfig(), false)
	stuck, rpc := d.open(445), d.open(4700)
	input := bytes.Repeat([]byte{0x71}, MaxData)
	for i := 0; i < (3<<20)/MaxData; i++ {
		d.frame(Frame{DataCommand, stuck.Channel, input})
	}
	casualties := 0
	for round := 0; round < 8; round++ {
		// Reclaimed holders cannot survive by definition. Reintroduce the same
		// pressure each interval; retain the eight 125ms completions,
		// 64KiB replenishment and eight one-second pressure rounds.
		if round > 0 {
			stuck = d.open(445)
			for i := 0; i < (3<<20)/MaxData; i++ {
				d.frame(Frame{DataCommand, stuck.Channel, input})
			}
		}
		healthy := d.open(445)
		w := Action{}
		for i := 0; i < (5<<20)/MaxData; i++ {
			out := d.frame(Frame{DataCommand, healthy.Channel, input})
			if i == 0 {
				w = mustAction(t, out, WriteLocal)
			}
		}
		for i := 0; i < 8; i++ {
			d.now = d.now.Add(time.Second / 8)
			w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: MaxData}, w)), WriteLocal)
		}
		for i := 0; i < 8; i++ {
			d.frame(Frame{DataCommand, healthy.Channel, input})
		}
		out := d.frame(Frame{DataCommand, rpc.Channel, []byte("rpc\n")})
		if c := mustAction(t, out, CloseLocal); c.Channel == healthy.Channel {
			casualties++
		}
		wr := mustAction(t, out, WriteLocal)
		d.step(local(Event{Kind: LocalWritten, Count: len(wr.Data)}, wr))
		if d.m.Channel(stuck.Channel).Active || !d.m.Channel(healthy.Channel).Active || d.m.Channel(healthy.Channel).Queued != 5<<20 || d.m.Status() != Active {
			t.Fatalf("round %d: stalled holder not reclaimed or progressing reconnect lost", round)
		}
		// Client-requested retirement permits the next healthy reconnect.
		d.step(local(Event{Kind: LocalClose}, healthy))
	}
	t.Logf("healthy transfers killed=%d in %s; remaining stuck backlog=%d; RPC/session remain live; usage=%+v", casualties, time.Duration(d.now), d.m.Channel(stuck.Channel).Queued, d.m.Usage())
	if casualties != 0 {
		t.Fatalf("progressing transfers repeatedly reclaimed: %d", casualties)
	}
}

func TestProgressingRPCSurvivesManyStuckReaders(t *testing.T) {
	d := activeDriver(t, legacyCapacityConfig(), false)
	rpc := d.open(4700)
	input := bytes.Repeat([]byte{0x71}, MaxData)
	w := Action{}
	for i := 0; i < (128<<10)/MaxData; i++ {
		out := d.frame(Frame{DataCommand, rpc.Channel, input})
		if i == 0 {
			w = mustAction(t, out, WriteLocal)
		}
	}
	holders := make([]Action, 127)
	remaining := d.m.cfg.SessionBytes - (128 << 10)
	for i := range holders {
		holders[i] = d.open(445)
		size := remaining / (len(holders) - i)
		remaining -= size
		for left := size; left > 0; {
			n := min(left, MaxData)
			d.frame(Frame{DataCommand, holders[i].Channel, input[:n]})
			left -= n
		}
	}
	for i := 0; i < 8; i++ {
		d.now = d.now.Add(time.Second / 8)
		w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: MaxData}, w)), WriteLocal)
	}
	for i := 0; i < 8; i++ {
		d.frame(Frame{DataCommand, rpc.Channel, input})
	}
	out := d.frame(Frame{DataCommand, rpc.Channel, []byte("\n")})
	t.Logf("RPC consumed=65536 in 1s, retained=131072 before pressure, stuck readers=127; close=%d RPC active=%v usage=%+v", mustAction(t, out, CloseLocal).Channel, d.m.Channel(rpc.Channel).Active, d.m.Usage())
	if !d.m.Channel(rpc.Channel).Active {
		t.Fatal("progressing RPC chosen while 127 stuck readers survive")
	}
}
