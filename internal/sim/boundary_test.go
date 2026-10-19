package sim_test

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

func TestExistingDestinationHorizonOverflowIsAtomic(t *testing.T) {
	n := sim.NewNetwork(1, 3, 3)
	if err := n.Send(sim.Packet{To: "a", Data: []byte{1}}, sim.Link{MTU: 1, ConsumerDelay: time.Duration(math.MaxInt64 - 10)}); err != nil {
		t.Fatal(err)
	}
	// The individual duration fits; adding it to admitted service does not.
	if err := n.Send(sim.Packet{To: "a", Data: []byte{2}}, sim.Link{MTU: 1, ConsumerDelay: 20}); !errors.Is(err, sim.ErrTime) {
		t.Fatalf("existing horizon overflow=%v; want ErrTime", err)
	}
	if b, c := n.Scheduler.Usage(); b != 1 || c != 1 {
		t.Fatalf("overflow changed usage=%d/%d; want 1/1", b, c)
	}
	var got []byte
	if err := n.Scheduler.Run(0, 1, func(p sim.Packet, at core.Instant) error {
		got = append(got, p.Data...)
		if at != 0 {
			t.Fatalf("delivery at %d; want 0", at)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []byte{1}) {
		t.Fatalf("delivery=%v; want only the admitted packet [1]", got)
	}
}

func TestDestinationHorizonRecoversAfterOutOfOrderDrain(t *testing.T) {
	n := sim.NewNetwork(1, 3, 3)
	for i, link := range []sim.Link{
		{MTU: 1, Delay: 1000, ConsumerDelay: 1},
		{MTU: 1, ConsumerDelay: 1000},
	} {
		if err := n.Send(sim.Packet{To: "a", Data: []byte{byte(i)}}, link); err != nil {
			t.Fatal(err)
		}
	}
	var got []byte
	var times []core.Instant
	if err := n.Scheduler.Run(1001, 4, func(p sim.Packet, at core.Instant) error {
		got = append(got, p.Data...)
		times = append(times, at)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []byte{1, 0}) || !reflect.DeepEqual(times, []core.Instant{0, 1000}) {
		t.Fatalf("drain order=%v times=%v; want [1 0] at [0 1000]", got, times)
	}
	if b, c := n.Scheduler.Usage(); b != 0 || c != 0 {
		t.Fatalf("drained usage=%d/%d; want 0/0", b, c)
	}
	// Admission order's conservative horizon was 2001; actual service ends at
	// 1001. A drained destination must recover that unused overflow credit.
	if err := n.Send(sim.Packet{To: "a", Data: []byte{2}}, sim.Link{MTU: 1, ConsumerDelay: time.Duration(math.MaxInt64 - 1500)}); err != nil {
		t.Fatalf("representable service after drain=%v; want admission", err)
	}
	if err := n.Scheduler.Run(1001, 1, func(p sim.Packet, at core.Instant) error {
		if !reflect.DeepEqual(p.Data, []byte{2}) || at != 1001 {
			t.Fatalf("post-drain delivery=%v at %d; want [2] at 1001", p.Data, at)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointCapWithSparePacketAndByteCredit(t *testing.T) {
	n := sim.NewNetwork(1, 10, 10)
	n.MaxEndpoints = 1
	link := sim.Link{MTU: 1}
	if err := n.Send(sim.Packet{To: "a", Data: []byte{1}}, link); err != nil {
		t.Fatal(err)
	}
	if err := n.Scheduler.Run(0, 1, func(sim.Packet, core.Instant) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// Destination metadata is retained after draining; the event budget is free.
	if err := n.Send(sim.Packet{To: "b", Data: []byte{2}}, link); !errors.Is(err, sim.ErrBudget) {
		t.Fatalf("new destination at endpoint cap=%v; want ErrBudget", err)
	}
	if b, c := n.Scheduler.Usage(); b != 0 || c != 0 {
		t.Fatalf("rejected endpoint changed usage=%d/%d; want 0/0", b, c)
	}
	if err := n.Send(sim.Packet{To: "a", Data: []byte{3}}, link); err != nil {
		t.Fatalf("existing destination at cap=%v; want admission", err)
	}
	var got []byte
	if err := n.Scheduler.Run(0, 1, func(p sim.Packet, _ core.Instant) error {
		got = append(got, p.Data...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []byte{3}) {
		t.Fatalf("delivery=%v; want existing destination only [3]", got)
	}
}

func TestTimerArmAtNow(t *testing.T) {
	for _, now := range []core.Instant{0, 7} {
		s := sim.New[string](0, 1)
		timer := sim.NewTimer(s)
		if err := s.Run(now, 1, func(string, core.Instant) error { return nil }); err != nil {
			t.Fatal(err)
		}
		// Replacement at full capacity must accept equality with Now as well.
		if err := timer.Arm(now+10, "old"); err != nil {
			t.Fatal(err)
		}
		if err := timer.Arm(s.Now(), "due"); err != nil {
			t.Fatalf("arm at Now=%d: %v; want admission", now, err)
		}
		if at, ok := timer.Deadline(); !ok || at != now {
			t.Fatalf("deadline=%d active=%v; want %d, true", at, ok, now)
		}
		if b, c := s.Usage(); b != 0 || c != 1 {
			t.Fatalf("armed usage=%d/%d; want 0/1", b, c)
		}
		var got []string
		handle := func(event string, at core.Instant) error {
			got = append(got, event)
			if at != now {
				t.Fatalf("timer fired at %d; want %d", at, now)
			}
			return nil
		}
		for i := 0; i < 2; i++ {
			if err := s.Run(now, 1, handle); err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(got, []string{"due"}) {
			t.Fatalf("delivery=%v; want [due] exactly once", got)
		}
		if _, ok := timer.Deadline(); ok {
			t.Fatal("fired timer still active")
		}
		if b, c := s.Usage(); b != 0 || c != 0 {
			t.Fatalf("fired usage=%d/%d; want 0/0", b, c)
		}
	}
}
