package p2ptunnel

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestSharedPressureDrainResumesEmptyChannel(t *testing.T) {
	c := DefaultConfig()
	c.HighWater = 8192
	d := activeDriver(t, c, false)
	ch := d.open(4700)
	d.step(Event{Kind: ReceiveQueueSize, Count: 8192})
	w := mustAction(t, d.frame(Frame{DataCommand, ch.Channel, []byte{1}}), WriteLocal)
	if !d.m.Channel(ch.Channel).SentPause {
		t.Fatal("setup: expected PAUSE")
	}
	d.step(local(Event{Kind: LocalWritten, Count: 1}, w))
	if s := d.m.Channel(ch.Channel); s.Queued != 0 || s.SentPause || !d.hasWire(ResumeCommand) {
		t.Fatalf("setup state: %+v", s)
	}
	d.step(Event{Kind: ReceiveQueueSize, Count: 0})
	d.now = core.Instant(time.Hour)
	d.step(Event{Kind: Tick})
	if d.m.Channel(ch.Channel).SentPause || !d.hasWire(ResumeCommand) {
		t.Fatalf("empty channel stayed paused after shared pressure cleared: state=%+v wire=%+v; want RESUME", d.m.Channel(ch.Channel), d.wire)
	}
}

func TestSharedPressureDrainOrders(t *testing.T) {
	for _, sharedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "local-first", true: "shared-first"}[sharedFirst], func(t *testing.T) {
			c := DefaultConfig()
			c.HighWater = 8192
			d := activeDriver(t, c, false)
			var writes []Action
			for i := 0; i < 4; i++ {
				ch := d.open(4700)
				writes = append(writes, mustAction(t, d.frame(Frame{DataCommand, ch.Channel, []byte{1}}), WriteLocal))
			}
			// Four equal retained holders divide shared pressure four ways.
			d.step(Event{Kind: ReceiveQueueSize, Count: 4 * 8192})
			for _, w := range writes {
				if !d.m.Channel(w.Channel).SentPause {
					t.Fatalf("channel %d did not pause on shared growth", w.Channel)
				}
			}
			d.wire = nil
			drain := func() {
				for _, w := range writes {
					d.step(local(Event{Kind: LocalWritten, Count: 1}, w))
				}
			}
			if sharedFirst {
				d.step(Event{Kind: ReceiveQueueSize, Count: 0})
				drain()
			} else {
				drain()
				d.step(Event{Kind: ReceiveQueueSize, Count: 0})
			}
			// No further DATA, completion, or Tick is needed to release any peer.
			for _, w := range writes {
				count := 0
				for _, f := range d.wire {
					if f.Command == ResumeCommand && f.Channel == w.Channel {
						count++
					}
				}
				if s := d.m.Channel(w.Channel); s.SentPause || s.Queued != 0 || count != 1 {
					t.Fatalf("channel %d: state=%+v resume count=%d; want empty, resumed once", w.Channel, s, count)
				}
			}
		})
	}
}

func TestSharedPressureAllSlots(t *testing.T) {
	c := DefaultConfig()
	c.HighWater = 8192
	d := activeDriver(t, c, false)
	for i := 0; i < 256; i++ {
		ch := d.open(4700)
		d.frame(Frame{DataCommand, ch.Channel, []byte{1}})
	}
	for _, count := range []int{256 * c.HighWater, 0} {
		d.wire = nil
		d.step(Event{Kind: ReceiveQueueSize, Count: count})
		command := PauseCommand
		if count == 0 {
			command = ResumeCommand
		}
		var transitions [256]int
		for _, f := range d.wire {
			if f.Command != command {
				t.Fatalf("shared=%d: command=%v; want %v", count, f.Command, command)
			}
			transitions[f.Channel]++
		}
		// Every retained holder, including slots 0 and 255, reacts to its
		// attributed shared pressure without a completion or Tick.
		for i := 0; i < 256; i++ {
			s := d.m.Channel(byte(i))
			if !s.Active || s.Queued != 1 || s.SentPause != (count != 0) || transitions[i] != 1 {
				t.Fatalf("slot %d shared=%d: state=%+v transitions=%d; want active, one retained byte, paused=%v, one %v", i, count, s, transitions[i], count != 0, command)
			}
		}
	}
}

func TestSharedPressureControlsHaveSeparateCapacity(t *testing.T) {
	c := DefaultConfig()
	c.TXFrames = 8
	c.HighWater = 100
	d := activeDriver(t, c, false)
	for i := 0; i < 256; i++ {
		ch := d.open(4700)
		d.frame(Frame{DataCommand, ch.Channel, []byte{1}})
	}
	d.auto = false
	d.step(Event{Kind: ReceiveQueueSize, Count: 256 * c.HighWater})
	if d.m.Status() != Active {
		t.Fatalf("flow controls exhausted DATA budget: status=%v", d.m.Status())
	}
	d.auto = true
	d.step(Event{Kind: Sent, Token: d.m.sendToken, Count: d.m.sendCount})
	count := 0
	for _, f := range d.wire {
		if f.Command == PauseCommand {
			count++
		}
	}
	if count != 255 {
		t.Fatalf("remaining PAUSE handoffs=%d want 255", count)
	}
}
