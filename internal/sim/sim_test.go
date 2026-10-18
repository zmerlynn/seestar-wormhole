package sim

import (
	"bytes"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"reflect"
	"testing"
	"time"
)

func TestSeededImpairment(t *testing.T) {
	run := func(seed uint64) ([]byte, int, int) {
		n := NewNetwork(seed, 4096, 1024)
		link := Link{MTU: 8, LossPPM: 250000, DuplicatePPM: 500000, Delay: time.Millisecond, Jitter: time.Second}
		for i := 0; i < 200; i++ {
			if e := n.Send(Packet{"a", "b", []byte{byte(i)}}, link); e != nil {
				t.Fatal(e)
			}
		}
		var seen []byte
		if e := n.Scheduler.Run(core.Instant(2*time.Second), 1024, func(p Packet, _ core.Instant) error { seen = append(seen, p.Data...); return nil }); e != nil {
			t.Fatal(e)
		}
		if b, c := n.Scheduler.Usage(); b != 0 || c != 0 {
			t.Fatal("not drained")
		}
		return seen, n.Dropped, n.Duplicated
	}
	a, l, d := run(42)
	b, l2, d2 := run(42)
	c, _, _ := run(43)
	if !bytes.Equal(a, b) || l != l2 || d != d2 || bytes.Equal(a, c) || l == 0 || d == 0 {
		t.Fatal("seed replay or impairment")
	}
	reordered := false
	for i := 1; i < len(a); i++ {
		if a[i] < a[i-1] {
			reordered = true
		}
	}
	if !reordered {
		t.Fatal("no reordering")
	}
}
func TestMTUBudgetAndSlowConsumer(t *testing.T) {
	n := NewNetwork(1, 4, 2)
	link := Link{MTU: 2, ConsumerDelay: time.Second}
	if e := n.Send(Packet{"a", "b", []byte{1, 2, 3}}, link); e != nil || n.Dropped != 1 {
		t.Fatal("MTU")
	}
	data := []byte{1, 2}
	if e := n.Send(Packet{"a", "b", data}, link); e != nil {
		t.Fatal(e)
	}
	data[0] = 9
	if e := n.Send(Packet{"a", "b", []byte{3, 4}}, link); e != nil {
		t.Fatal(e)
	}
	if e := n.Send(Packet{"a", "b", data}, link); !errors.Is(e, ErrBudget) {
		t.Fatal("budget")
	}
	var times []core.Instant
	var got []byte
	if e := n.Scheduler.Run(core.Instant(2*time.Second), 4, func(p Packet, now core.Instant) error {
		times = append(times, now)
		got = append(got, p.Data...)
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(times, []core.Instant{0, core.Instant(time.Second)}) || !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Fatal(times, got)
	}
	if n.Scheduler.PeakBytes != 4 || n.Scheduler.PeakCount != 2 {
		t.Fatal("peak accounting")
	}
}
func TestSchedulerOrderClockAndLimits(t *testing.T) {
	s := New[int](2, 2)
	for i := 0; i < 2; i++ {
		if e := s.Schedule(10, i, 1); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.Schedule(10, 3, 1); !errors.Is(e, ErrBudget) {
		t.Fatal(e)
	}
	s.JumpWall(-time.Hour)
	var order []int
	if e := s.Run(10, 1, func(v int, _ core.Instant) error { order = append(order, v); return nil }); e == nil {
		t.Fatal("step limit")
	}
	if e := s.Run(10, 1, func(v int, _ core.Instant) error { order = append(order, v); return nil }); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(order, []int{0, 1}) || s.Now() != 10 || s.Wall() != -time.Hour+10 {
		t.Fatal("clock or tie break")
	}
	if e := s.Schedule(9, 0, 0); !errors.Is(e, ErrTime) {
		t.Fatal(e)
	}
}
func TestAtomicDuplicateBudget(t *testing.T) {
	n := NewNetwork(1, 1, 1)
	if e := n.Send(Packet{"a", "b", []byte{1}}, Link{MTU: 1, DuplicatePPM: 1000000}); !errors.Is(e, ErrBudget) {
		t.Fatal(e)
	}
	if b, c := n.Scheduler.Usage(); b != 0 || c != 0 {
		t.Fatal("partial scheduling")
	}
}

func TestNetworkTimeOverflowIsAtomic(t *testing.T) {
	n := NewNetwork(1, 10, 10)
	if e := n.Scheduler.Run(core.Instant(1<<63-2), 1, func(Packet, core.Instant) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if e := n.Send(Packet{"a", "b", []byte{1}}, Link{MTU: 1, Delay: time.Second}); !errors.Is(e, ErrTime) {
		t.Fatal(e)
	}
	if b, c := n.Scheduler.Usage(); b != 0 || c != 0 {
		t.Fatal("partial overflow scheduling")
	}
	if e := n.Send(Packet{"a", "b", nil}, Link{MTU: 1, DuplicatePPM: 1000000, ConsumerDelay: time.Nanosecond}); !errors.Is(e, ErrTime) {
		t.Fatal(e)
	}
	if b, c := n.Scheduler.Usage(); b != 0 || c != 0 {
		t.Fatal("partial duplicate overflow")
	}
}

func TestAtomicDuplicateCountWithAmpleBytes(t *testing.T) {
	for _, data := range [][]byte{{1}, nil} {
		n := NewNetwork(1, 1024, 2)
		if e := n.Send(Packet{"a", "b", data}, Link{MTU: 1}); e != nil {
			t.Fatal(e)
		}
		beforeBytes, beforeCount := n.Scheduler.Usage()
		if e := n.Send(Packet{"a", "b", data}, Link{MTU: 1, DuplicatePPM: 1000000}); !errors.Is(e, ErrBudget) {
			t.Fatalf("duplicate admission=%v; want budget rejection", e)
		}
		if b, c := n.Scheduler.Usage(); b != beforeBytes || c != beforeCount {
			t.Fatalf("partial duplicate queued: %d/%d; want %d/%d", b, c, beforeBytes, beforeCount)
		}
		delivered := 0
		if e := n.Scheduler.Run(0, 10, func(Packet, core.Instant) error { delivered++; return nil }); e != nil {
			t.Fatal(e)
		}
		if delivered != 1 || n.Duplicated != 0 {
			t.Fatalf("delivered=%d duplicates=%d; want 1/0", delivered, n.Duplicated)
		}
	}
}
