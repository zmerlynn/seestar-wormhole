package p2ptunnel

import (
	"bytes"
	"runtime"
	"testing"
)

// Growing the receive envelope must not copy a multi-MiB backlog on every DATA
// and local completion. Measure cumulative allocations, with generous headroom
// for runtime/test overhead; the former contiguous queue copies >120 MiB here.
func TestReceiveQueueCopyCost(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	ch := d.open(445)
	// Explicit larger bounds isolate queue work from the default capacity policy.
	d.m.cfg.ChannelBytes, d.m.cfg.SessionBytes = 2<<20, 4<<20
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	input := bytes.Repeat([]byte{0x17}, MaxData)
	first := mustAction(t, d.frame(Frame{DataCommand, ch.Channel, input}), WriteLocal)
	for i := 1; i < 128; i++ {
		d.frame(Frame{DataCommand, ch.Channel, input})
	}
	a := first
	for i := 0; i < 128; i++ {
		if !bytes.Equal(a.Data, input) {
			t.Fatal("queue corrupt")
		}
		out := d.step(local(Event{Kind: LocalWritten, Count: len(a.Data)}, a))
		if i < 127 {
			a = mustAction(t, out, WriteLocal)
		}
	}
	runtime.ReadMemStats(&after)
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32<<20 {
		t.Fatalf("copied backlog repeatedly: allocated=%d for 1 MiB burst, want <=32 MiB", allocated)
	}
	if d.m.Usage().RXBytes != 0 {
		t.Fatal("queue did not drain")
	}
}

func TestReceiveQueueFragmentation(t *testing.T) {
	var q receiveQueue
	input := bytes.Repeat([]byte("abc"), 3000)
	for _, b := range input {
		q.push([]byte{b})
	}
	if q.size != len(input) || q.capacity() != len(input) || len(q.chunks) != (len(input)+MaxData-1)/MaxData {
		t.Fatal("unbounded chunk fragmentation")
	}
	var got []byte
	for q.size > 0 {
		out := q.front()
		n := min(len(out), 997)
		got = append(got, out[:n]...)
		q.consume(n)
		if q.capacity() != q.size {
			t.Fatal("partial drain retained capacity")
		}
	}
	if !bytes.Equal(got, input) || len(q.chunks) != 0 {
		t.Fatal("corrupt bytes or retained chunk pointers")
	}
}
