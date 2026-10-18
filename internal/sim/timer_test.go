package sim

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"reflect"
	"testing"
)

func TestTimerReplacementCancellationAndOrder(t *testing.T) {
	s := New[string](0, 2)
	timer := NewTimer(s)
	if err := timer.Arm(10, "old"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if err := timer.Arm(20, "current"); err != nil {
			t.Fatal(err)
		}
	}
	if b, c := s.Usage(); b != 0 || c != 1 || s.PeakCount != 1 {
		t.Fatalf("replacements accumulated: %d/%d peak=%d", b, c, s.PeakCount)
	}
	if at, ok := timer.Deadline(); !ok || at != 20 {
		t.Fatalf("deadline=%d active=%v", at, ok)
	}
	if err := s.Schedule(20, "input", 0); err != nil {
		t.Fatal(err)
	}
	var got []string
	handle := func(e string, now core.Instant) error {
		got = append(got, e)
		if now != 20 {
			t.Fatalf("delivery at %d; want 20", now)
		}
		return nil
	}
	if err := s.Run(19, 2, handle); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("early or stale replaced delivery", got)
	}
	if err := s.Run(20, 2, handle); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"current", "input"}) {
		t.Fatalf("order=%v", got)
	}
	if _, ok := timer.Deadline(); ok {
		t.Fatal("fired timer still active")
	}
	if err := timer.Arm(30, "canceled"); err != nil {
		t.Fatal(err)
	}
	if err := timer.Arm(19, "past"); err != ErrTime {
		t.Fatalf("past arm=%v", err)
	}
	if at, ok := timer.Deadline(); !ok || at != 30 {
		t.Fatal("failed replacement lost timer")
	}
	timer.Cancel()
	timer.Cancel()
	if err := s.Run(100, 1, handle); err != nil {
		t.Fatal(err)
	}
	if _, c := s.Usage(); c != 0 || len(got) != 2 {
		t.Fatal("canceled timer retained or delivered")
	}
}
