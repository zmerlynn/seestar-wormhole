package edge

import "testing"

func TestReviewDefaultOffAlloc(t *testing.T) {
	p, err := NewDTLSPackets(1388)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.allowApplication()
	allocs := testing.AllocsPerRun(1000, func() {
		wire := [14]byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 42}
		if err := p.Deliver(1, wire[:]); err != nil {
			panic(err)
		}
		var buf [1388]byte
		if _, _, err := p.ReadFrom(buf[:]); err != nil {
			panic(err)
		}
	})
	// Pion's record slices and the borrowed input now escape during decoding,
	// in addition to the owned packet copy. No buffer reaches the trace hook.
	if allocs != 3 {
		t.Fatalf("Deliver+ReadFrom allocations = %v, want Pion admission baseline 3", allocs)
	}
	t.Logf("trace absent Deliver+ReadFrom allocs/run=%.0f", allocs)
}

// Delivery includes Pion framing; read/write retain the original trace baselines.
// Buffers live inside each measured call to expose optional-hook escapes.
func TestTraceOffPacketAllocationBaselines(t *testing.T) {
	p, err := NewDTLSPackets(1388)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p.allowApplication()
	packet := []byte{42}
	tests := []struct {
		name string
		want float64
		run  func()
	}{
		{"Deliver", 3, func() {
			wire := [14]byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 42}
			if err := p.Deliver(1, wire[:]); err != nil {
				panic(err)
			}
			<-p.rx
		}},
		{"ReadFrom", 0, func() {
			p.rx <- packet
			var buf [1388]byte
			if _, _, err := p.ReadFrom(buf[:]); err != nil {
				panic(err)
			}
		}},
		{"WriteTo", 1, func() {
			wire := [14]byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 42}
			if _, err := p.WriteTo(wire[:], nil); err != nil {
				panic(err)
			}
			<-p.Outgoing()
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := testing.AllocsPerRun(1000, tc.run)
			t.Logf("allocations=%v baseline=%v", got, tc.want)
			if got != tc.want {
				t.Fatalf("allocations=%v want baseline %v", got, tc.want)
			}
		})
	}
}
