package p2ptunnel

import (
	"bytes"
	"testing"
)

// Generate another WAKE across every byte-completion position of each possible
// predecessor. In particular RESUME may finish after renewed high pressure:
// its successor PAUSE must complete before the retained WAKE opportunity.
func TestWakeAcrossEveryPredecessorCompletion(t *testing.T) {
	var permutations func([]byte, int, int, func([]byte))
	permutations = func(order []byte, w, c int, run func([]byte)) {
		if w == 2 && c == 4 {
			run(order)
			return
		}
		if w < 2 {
			permutations(append(bytes.Clone(order), 'W'), w+1, c, run)
		}
		if c < 4 {
			permutations(append(bytes.Clone(order), 'C'), w, c+1, run)
		}
	}
	for _, predecessor := range []byte{5, 6, 7} {
		permutations(nil, 0, 0, func(order []byte) {
			cfg := DefaultConfig()
			cfg.HighWater = 100
			d := activeDriver(t, cfg, false)
			ch := d.open(445)
			if predecessor == 5 {
				d.auto = false
			}
			var write, send Action
			fill := func() {
				for i := 0; i < 5; i++ {
					out := d.frame(Frame{DataCommand, ch.Channel, bytes.Repeat([]byte{'b'}, MaxData)})
					if a, ok := find(out, WriteLocal); ok {
						write = a
					}
					if a, ok := find(out, Send); ok {
						send = a
					}
				}
			}
			fill()
			if predecessor == 7 {
				d.auto = false
			}
			written := func(n int) {
				out := d.step(local(Event{Kind: LocalWritten, Count: n}, write))
				if a, ok := find(out, WriteLocal); ok {
					write = a
				}
				if a, ok := find(out, Send); ok {
					send = a
				}
			}
			if predecessor == 6 {
				for i := 0; i < 5; i++ {
					if i == 4 {
						d.auto = false
					}
					written(MaxData)
				}
				fill()
			}
			if predecessor == 7 {
				written(MaxData)
				written(5)
			}
			if send.Data[0] != predecessor {
				t.Fatalf("setup predecessor=%d got=%x", predecessor, send.Data)
			}
			var wire []byte
			complete := func(n int) {
				old := send
				send = Action{}
				wire = append(wire, old.Data[:n]...)
				out := d.step(Event{Kind: Sent, Token: old.Token, Count: n})
				if a, ok := find(out, Send); ok {
					send = a
				}
			}
			writes := 0
			for _, op := range order {
				if op == 'C' {
					complete(1)
				} else {
					n := MaxData
					if writes > 0 {
						n = 5
					}
					writes++
					written(n)
				}
			}
			for i := 0; send.Token != 0 && i < 3; i++ {
				complete(len(send.Data))
			}
			want := []byte{predecessor, ch.Channel, 0, 0}
			if predecessor == 6 {
				want = append(want, 5, ch.Channel, 0, 0)
			}
			want = append(want, 7, ch.Channel, 0, 0)
			if !bytes.Equal(wire, want) || send.Token != 0 {
				t.Fatalf("predecessor=%d order=%s accepted=%x want=%x next=%x", predecessor, order, wire, want, send.Data)
			}
		})
	}
}
