package sim_test

import (
	"reflect"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

func TestDrainedDestinationNeedsNoServiceEvent(t *testing.T) {
	n := sim.NewNetwork(1, 3, 3)
	for i := 0; i < 2; i++ {
		if err := n.Send(sim.Packet{To: "a", Data: []byte{byte(i)}}, sim.Link{MTU: 1, ConsumerDelay: 1}); err != nil {
			t.Fatal(err)
		}
	}
	var got []byte
	handle := func(p sim.Packet, _ core.Instant) error {
		got = append(got, p.Data...)
		return nil
	}
	// Two arrivals and one deferred service drain the paced backlog. Advance
	// to its final ready time so the next arrival needs no service event.
	if err := n.Scheduler.Run(2, 3, handle); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []byte{0, 1}) {
		t.Fatalf("backlog delivery=%v; want [0 1]", got)
	}
	if b, c := n.Scheduler.Usage(); b != 0 || c != 0 {
		t.Fatalf("drained usage=%d/%d; want 0/0", b, c)
	}
	if err := n.Send(sim.Packet{To: "a", Data: []byte{2}}, sim.Link{MTU: 1}); err != nil {
		t.Fatal(err)
	}
	if err := n.Scheduler.Run(2, 1, handle); err != nil {
		t.Fatalf("unpaced arrival after drain=%v; want delivery in one step", err)
	}
	if !reflect.DeepEqual(got, []byte{0, 1, 2}) {
		t.Fatalf("post-drain delivery=%v; want [0 1 2]", got)
	}
	if b, c := n.Scheduler.Usage(); b != 0 || c != 0 {
		t.Fatalf("delivered usage=%d/%d; want 0/0", b, c)
	}
}
