package sim

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"reflect"
	"testing"
	"time"
)

func TestReviewSlowConsumerPreservesArrivalReordering(t *testing.T) {
	n := NewNetwork(1, 10, 10)
	for i, delay := range []time.Duration{10 * time.Second, time.Second} {
		if err := n.Send(Packet{From: "a", To: "b", Data: []byte{byte(i)}}, Link{MTU: 1, Delay: delay, ConsumerDelay: time.Second}); err != nil {
			t.Fatal(err)
		}
	}
	var order []byte
	var times []core.Instant
	if err := n.Scheduler.Run(core.Instant(20*time.Second), 10, func(p Packet, at core.Instant) error {
		order = append(order, p.Data[0])
		times = append(times, at)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []byte{1, 0}) || !reflect.DeepEqual(times, []core.Instant{core.Instant(time.Second), core.Instant(10 * time.Second)}) {
		t.Fatalf("delivery order=%v times=%v; want order=[1 0] times=[1000000000 10000000000] (arrival order before pacing)", order, times)
	}
}

func TestJitterReorderPacingTwoDestinations(t *testing.T) {
	type seen struct {
		To   string
		Data byte
		At   core.Instant
	}
	run := func(seed uint64, paced bool) ([]seen, int, int) {
		n := NewNetwork(seed, 64, 64)
		for i := 0; i < 32; i++ {
			for _, dst := range []string{"slow", "fast"} {
				delay := time.Duration(0)
				if paced && dst == "slow" {
					delay = time.Second
				}
				if err := n.Send(Packet{"sender", dst, []byte{byte(i)}}, Link{MTU: 1, Delay: time.Second, Jitter: 3 * time.Second, ConsumerDelay: delay}); err != nil {
					t.Fatal(err)
				}
			}
		}
		var got []seen
		if err := n.Scheduler.Run(core.Instant(40*time.Second), 256, func(p Packet, at core.Instant) error { got = append(got, seen{p.To, p.Data[0], at}); return nil }); err != nil {
			t.Fatal(err)
		}
		if b, c := n.Scheduler.Usage(); b != 0 || c != 0 {
			t.Fatalf("undrained: %d/%d", b, c)
		}
		return got, n.Scheduler.PeakBytes, n.Scheduler.PeakCount
	}
	base, _, _ := run(42, false)
	got, b, c := run(42, true)
	replay, _, _ := run(42, true)
	if !reflect.DeepEqual(got, replay) || b != 64 || c != 64 {
		t.Fatalf("replay or budget: peaks %d/%d", b, c)
	}
	for _, dst := range []string{"slow", "fast"} {
		var arrivals, deliveries []seen
		for _, x := range base {
			if x.To == dst {
				arrivals = append(arrivals, x)
			}
		}
		for _, x := range got {
			if x.To == dst {
				deliveries = append(deliveries, x)
			}
		}
		var ready core.Instant
		reordered := false
		for i, x := range arrivals {
			at := max(x.At, ready)
			if dst == "slow" {
				ready = at.Add(time.Second)
			}
			if deliveries[i].Data != x.Data || deliveries[i].At != at {
				t.Fatalf("%s delivery %d=%+v; want data %d at %d", dst, i, deliveries[i], x.Data, at)
			}
			if i > 0 && x.Data < arrivals[i-1].Data {
				reordered = true
			}
		}
		if !reordered {
			t.Fatalf("%s did not exercise reordering", dst)
		}
	}
}

func TestDestinationBacklogRemainsCharged(t *testing.T) {
	n := NewNetwork(1, 3, 3)
	link := Link{MTU: 1, ConsumerDelay: time.Hour}
	for i := 0; i < 3; i++ {
		if e := n.Send(Packet{"a", "slow", []byte{byte(i)}}, link); e != nil {
			t.Fatal(e)
		}
	}
	var got []byte
	if e := n.Scheduler.Run(0, 10, func(p Packet, _ core.Instant) error { got = append(got, p.Data[0]); return nil }); e != nil {
		t.Fatal(e)
	}
	if b, c := n.Scheduler.Usage(); b != 2 || c != 2 {
		t.Fatalf("waiting service not charged: %d/%d", b, c)
	}
	if e := n.Send(Packet{"a", "fast", []byte{3}}, Link{MTU: 1}); e != nil {
		t.Fatal(e)
	}
	if e := n.Send(Packet{"a", "slow", []byte{4}}, link); e != ErrBudget {
		t.Fatalf("backlog admission=%v; want budget rejection", e)
	}
	if e := n.Scheduler.Run(core.Instant(3*time.Hour), 20, func(p Packet, _ core.Instant) error { got = append(got, p.Data[0]); return nil }); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(got, []byte{0, 3, 1, 2}) {
		t.Fatalf("delivery=%v", got)
	}
}

func TestArrivalAtServiceDeadlineCannotOvertakeQueuedPacket(t *testing.T) {
	n := NewNetwork(1, 3, 3)
	for i, l := range []Link{
		{MTU: 1, ConsumerDelay: time.Second},
		{MTU: 1},
		{MTU: 1, Delay: time.Second},
	} {
		if err := n.Send(Packet{"a", "b", []byte{byte(i)}}, l); err != nil {
			t.Fatal(err)
		}
	}
	var got []byte
	if err := n.Scheduler.Run(core.Instant(time.Second), 10, func(p Packet, _ core.Instant) error { got = append(got, p.Data[0]); return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []byte{0, 1, 2}) {
		t.Fatalf("arrival overtook queued service: %v; want [0 1 2]", got)
	}
}
