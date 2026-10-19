package p2ptunnel

import (
	"bytes"
	"fmt"
	"testing"
)

// Exhaust all 1820 order-preserving permutations of four local completions
// and twelve control-byte completions. Twelve bytes cover PAUSE/WAKE/RESUME.
// Repeat with whole-frame acceptance and zero acceptance at every byte offset.
// The reference state is derived only from literal accepted bytes and valid
// local progress, never from Machine's desired/accepted/pending fields.
// PAUSE precedes its WAKE opportunity; WAKE does not clear pause; RESUME
// supersedes an unsent WAKE. A started frame
// must finish even if superseded. Zero acceptance may cancel an unstarted frame.
func TestFlowHandoffCompletionPermutations(t *testing.T) {
	cases := 0
	var visit func([]byte, int, int)
	visit = func(order []byte, writes, completions int) {
		if writes < 4 {
			visit(append(bytes.Clone(order), 'W'), writes+1, completions)
		}
		if completions < 12 {
			visit(append(bytes.Clone(order), 'C'), writes, completions+1)
		}
		if writes != 4 || completions != 12 {
			return
		}
		for _, mode := range []string{"byte", "whole", "zero-byte"} {
			cases++
			cfg := DefaultConfig()
			cfg.HighWater = 100
			d := activeDriver(t, cfg, false)
			ch := d.open(445)
			d.auto = false
			out := d.frame(Frame{DataCommand, ch.Channel, bytes.Repeat([]byte{'b'}, MaxData)})
			send := mustAction(t, out, Send)
			write := mustAction(t, out, WriteLocal)
			d.frame(Frame{DataCommand, ch.Channel, bytes.Repeat([]byte{'b'}, MaxData)})
			d.frame(Frame{DataCommand, ch.Channel, bytes.Repeat([]byte{'b'}, MaxData)})
			desired, handed, wake := true, false, false
			retained, accumulator, wi := 3*MaxData, 0, 0
			frame := []byte{5, ch.Channel, 0, 0}
			offset := 0
			counts := []int{MaxData, 5, MaxData, MaxData - 5}
			fail := func(format string, args ...any) {
				t.Fatalf("order=%s mode=%s: %s", order, mode, fmt.Sprintf(format, args...))
			}
			expected := func() byte {
				if desired != handed {
					if desired {
						return 5
					}
					return 6
				}
				if desired && wake {
					return 7
				}
				return 0
			}
			check := func(out []Action) {
				if a, ok := find(out, WriteLocal); ok {
					write = a
				}
				next, ok := find(out, Send)
				if send.Token != 0 {
					if ok {
						fail("second outstanding handoff")
					}
					return
				}
				if offset > 0 {
					if !ok || !bytes.Equal(next.Data, frame[offset:]) {
						fail("partial frame=%x offset=%d got=%x", frame, offset, next.Data)
					}
				} else {
					cmd := expected()
					if (cmd != 0) != ok {
						fail("pending control=%02x got Send=%v (%x)", cmd, ok, next.Data)
					}
					if !ok {
						return
					}
					frame = []byte{cmd, ch.Channel, 0, 0}
					if !bytes.Equal(next.Data, frame) {
						fail("next=%x want=%x", next.Data, frame)
					}
					if cmd == 7 {
						wake = false
					}
				}
				send = next
			}
			accept := func() {
				if send.Token == 0 {
					check(d.step(Event{Kind: TransportWritable}))
					if send.Token == 0 {
						return
					}
				}
				if mode == "zero-byte" {
					old := send
					send = Action{}
					if offset == 0 && frame[0] == 7 && desired && handed {
						wake = true
					}
					out := d.step(Event{Kind: Sent, Token: old.Token, Count: 0})
					if _, ok := find(out, Send); ok {
						fail("zero completion did not await credit")
					}
					check(d.step(Event{Kind: TransportWritable}))
					if send.Token == 0 {
						return
					}
				}
				n := 1
				if mode == "whole" {
					n = len(send.Data)
				}
				old := send
				send = Action{}
				offset += n
				if offset == 4 {
					switch frame[0] {
					case 5:
						handed = true
					case 6:
						handed = false
					case 7:
						if !handed {
							fail("WAKE accepted before PAUSE")
						}
					}
					offset = 0
				}
				check(d.step(Event{Kind: Sent, Token: old.Token, Count: n}))
			}
			for _, op := range order {
				if op == 'C' {
					accept()
					continue
				}
				n := counts[wi]
				wi++
				retained -= n
				accumulator = min(accumulator+n, 8197)
				if retained < 80 && desired {
					desired = false
					wake = false
				}
				if desired && retained > 80 && accumulator > 8196 {
					wake = true
					accumulator = 0
				}
				check(d.step(local(Event{Kind: LocalWritten, Count: n}, write)))
			}
			for i := 0; send.Token != 0 && i < 12; i++ {
				accept()
			}
			if send.Token != 0 || handed || desired || wake || d.m.Channel(ch.Channel).Queued != 0 {
				fail("final handoff/queue did not converge")
			}
		}
	}
	visit(nil, 0, 0)
	if cases != 5460 {
		t.Fatalf("permutation cases=%d want 5460", cases)
	}
	t.Logf("checked %d completion schedules", cases)
}
