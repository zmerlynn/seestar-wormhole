package p2ptunnel

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

// Model SMB-like pipelining as opaque bytes. Four MiB of replies can already be
// handed to device RDT before PAUSE arrives. This is not an
// SMB implementation. HTTP is likewise just a continuous opaque byte stream.
func TestCapacityBulkReaders(t *testing.T) {
	for _, shape := range []struct {
		name        string
		rate, burst int
	}{
		{"slow-pipelined", 64 << 10, 4 << 20},
		{"fast-pipelined", 32 << 20, 4 << 20},
		{"http-continuous", 32 << 20, 4 << 20},
		{"stuck", 0, 4 << 20},
	} {
		t.Run(shape.name, func(t *testing.T) {
			d := activeDriver(t, legacyCapacityConfig(), false)
			bulk, rpc := d.open(445), d.open(4700)
			type completion struct {
				a     Action
				count int
			}
			q := sim.New[completion](0, 8)
			const volume = 16594560
			source, sink := sha256.New(), sha256.New()
			sent, received, peak, rpcReplies := 0, 0, 0, 0
			var pending *Action
			capture := func(actions []Action) {
				for _, a := range actions {
					if a.Kind == CloseLocal || a.Kind == SessionClosed {
						t.Fatalf("%s after %d bytes", a.Reason, received)
					}
					if a.Kind == WriteLocal {
						if a.Channel == rpc.Channel {
							if string(a.Data) != "rpc\n" {
								t.Fatal("RPC corrupt")
							}
							if err := q.Schedule(q.Now(), completion{a, len(a.Data)}, 0); err != nil {
								t.Fatal(err)
							}
							rpcReplies++
						} else {
							copy := a
							pending = &copy
						}
					}
				}
				assertBudget(t, d.m)
				peak = max(peak, d.m.Usage().RXCapacity)
			}
			inject := func(n int) {
				for n > 0 {
					size := min(MaxData, n)
					b := make([]byte, size)
					for i := range b {
						b[i] = byte((sent+i)*31 + 7)
					}
					source.Write(b)
					sent += size
					n -= size
					capture(d.frame(Frame{DataCommand, bulk.Channel, b}))
				}
			}
			// Already-enqueued replies are delivered despite the emitted PAUSE. Socket
			// completions occur on a later simulator turn, even for a fast reader.
			inject(min(shape.burst, volume))
			for round := 0; round < 100000; round++ {
				if round%32 == 0 {
					capture(d.frame(Frame{DataCommand, rpc.Channel, []byte("rpc\n")}))
				}
				until := q.Now()
				if shape.rate == 0 {
					until = until.Add(time.Second)
				}
				if pending != nil && shape.rate > 0 {
					a := *pending
					pending = nil
					sink.Write(a.Data)
					received += len(a.Data)
					delay := time.Duration(len(a.Data)) * time.Second / time.Duration(shape.rate)
					until = q.Now().Add(delay)
					if err := q.Schedule(until, completion{a, len(a.Data)}, 0); err != nil {
						t.Fatal(err)
					}
				}
				if err := q.Run(until, 10000, func(c completion, now core.Instant) error {
					d.now = now
					capture(d.step(local(Event{Kind: LocalWritten, Count: c.count}, c.a)))
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				// Pending SMB requests can queue another reply while the local consumer
				// drains. Respect PAUSE for new socket reads, unlike the saved initial burst.
				if sent < volume && !d.m.Channel(bulk.Channel).SentPause {
					inject(min(shape.burst, volume-sent))
				}
				if shape.rate == 0 {
					if round == 63 {
						break
					}
				} else if received == volume && d.m.Channel(bulk.Channel).Queued == 0 {
					break
				}
			}
			if shape.rate > 0 && (received != volume || !bytes.Equal(source.Sum(nil), sink.Sum(nil))) {
				t.Fatalf("received %d/%d intact bytes", received, volume)
			}
			if shape.rate > 0 {
				want := time.Duration(volume) * time.Second / time.Duration(shape.rate)
				if elapsed := time.Duration(q.Now()); elapsed > want+time.Millisecond || elapsed < want-time.Millisecond {
					t.Fatalf("consumer pacing: elapsed=%s want=%s", elapsed, want)
				}
			}
			if rpcReplies < 2 || peak > d.m.cfg.SessionBytes {
				t.Fatalf("RPC=%d peak=%d", rpcReplies, peak)
			}
			t.Logf("sent=%d received=%d peak=%d RPC=%d elapsed=%s", sent, received, peak, rpcReplies, time.Duration(q.Now()))
		})
	}
}

// Two stuck readers retain their 4 MiB bursts until the aggregate budget is
// exhausted, while the RPC reader still completes.
func TestCapacityTwoStuckReadersPreserveRPC(t *testing.T) {
	d := activeDriver(t, legacyCapacityConfig(), false)
	a, b, rpc := d.open(445), d.open(445), d.open(4700)
	payload := bytes.Repeat([]byte{0x17}, MaxData)
	var oldWrite Action
	for _, ch := range []Action{a, b} {
		for i := 0; i < (4<<20)/MaxData; i++ {
			out := d.frame(Frame{DataCommand, ch.Channel, payload})
			if i == 0 && ch.Channel == a.Channel {
				oldWrite = mustAction(t, out, WriteLocal)
			}
		}
		if state := d.m.Channel(ch.Channel); !state.Active || !state.SentPause || state.Queued != 4<<20 {
			t.Fatalf("stuck burst: %+v", state)
		}
	}
	out := d.frame(Frame{DataCommand, rpc.Channel, []byte("rpc\n")})
	w := mustAction(t, out, WriteLocal)
	if w.Channel != rpc.Channel || string(w.Data) != "rpc\n" {
		t.Fatalf("RPC delivery: %+v", w)
	}
	closed := mustAction(t, out, CloseLocal)
	if closed.Channel != a.Channel || d.m.Channel(a.Channel).Active || !d.m.Channel(b.Channel).Active || !d.m.Channel(rpc.Channel).Active {
		t.Fatalf("largest-backlog tie must close lowest ID %d: close=%+v usage=%+v", a.Channel, closed, d.m.Usage())
	}
	if d.m.Usage().RXBytes != (4<<20)+4 {
		t.Fatalf("victim backlog not reclaimed: %+v", d.m.Usage())
	}
	// A completion already in flight for the reclaimed channel must be harmless.
	d.step(local(Event{Kind: LocalWritten, Count: MaxData}, oldWrite))
	d.step(local(Event{Kind: LocalWritten, Count: len(w.Data)}, w))
	mustAction(t, d.frame(Frame{DataCommand, rpc.Channel, []byte("ok\n")}), WriteLocal)
	if d.m.Status() != Active {
		t.Fatal("aggregate pressure stopped shared stream")
	}
}

func TestCapacityManyStuckReadersPreserveRPC(t *testing.T) {
	for _, n := range []int{2, 3, 8, 32, 127, 128, 255} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			d := activeDriver(t, legacyCapacityConfig(), false)
			stuck := make([]Action, n)
			for i := range stuck {
				stuck[i] = d.open(445)
			}
			rpc := d.open(4700)
			remaining := d.m.cfg.SessionBytes
			largest := stuck[0].Channel
			largestBytes := 0
			for i, ch := range stuck {
				size := remaining / (n - i)
				remaining -= size
				if size > largestBytes {
					largest, largestBytes = ch.Channel, size
				}
				for left := size; left > 0; {
					count := min(left, MaxData)
					d.frame(Frame{DataCommand, ch.Channel, make([]byte, count)})
					left -= count
				}
				if state := d.m.Channel(ch.Channel); !state.Active || state.Queued != size {
					t.Fatalf("reader %d: %+v", i, state)
				}
			}
			if d.m.Usage().RXBytes != d.m.cfg.SessionBytes {
				t.Fatal("did not fill aggregate")
			}
			// A tiny existing backlog is as innocent as an empty queue. First pressure
			// must reclaim the largest existing holder; subsequent RPC remains live.
			out := d.frame(Frame{DataCommand, rpc.Channel, []byte("r")})
			if close := mustAction(t, out, CloseLocal); close.Channel != largest {
				t.Fatalf("victim=%d want largest %d", close.Channel, largest)
			}
			w := mustAction(t, out, WriteLocal)
			if w.Channel != rpc.Channel {
				t.Fatal("RPC not delivered")
			}
			d.frame(Frame{DataCommand, rpc.Channel, []byte("pc\n")})
			if !d.m.Channel(rpc.Channel).Active || d.m.Status() != Active {
				t.Fatal("innocent RPC/session closed")
			}
			assertBudget(t, d.m)
		})
	}
}

func TestCapacityAggregateReclaimsMultipleVictims(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ChannelBytes = MaxData
	cfg.SessionBytes = MaxData
	cfg.HighWater = 1024
	d := activeDriver(t, cfg, false)
	a, b, c, rpc := d.open(445), d.open(445), d.open(445), d.open(4700)
	for i, ch := range []Action{a, b, c} {
		size := MaxData / 4
		if i == 0 {
			size *= 2
		}
		d.frame(Frame{DataCommand, ch.Channel, make([]byte, size)})
	}
	out := d.frame(Frame{DataCommand, rpc.Channel, make([]byte, MaxData)})
	var closed []byte
	for _, a := range out {
		if a.Kind == CloseLocal {
			closed = append(closed, a.Channel)
		}
	}
	if !bytes.Equal(closed, []byte{a.Channel, b.Channel, c.Channel}) {
		t.Fatalf("victim order=%v want [%d %d %d]", closed, a.Channel, b.Channel, c.Channel)
	}
	if w := mustAction(t, out, WriteLocal); w.Channel != rpc.Channel || d.m.Usage().RXBytes != MaxData {
		t.Fatal("RPC frame not admitted within reclaimed bound")
	}
}

// All 256 slots can retain a stalled delivery. The small last slot must survive
// pressure held by the other 255, even at the core's hard channel limit.
func TestCapacityAllChannelSlots(t *testing.T) {
	d := activeDriver(t, legacyCapacityConfig(), false)
	slots := make([]Action, 256)
	for i := range slots {
		slots[i] = d.open(445)
	}
	rpc := slots[255]
	d.frame(Frame{DataCommand, rpc.Channel, []byte("r")})
	remaining := d.m.cfg.SessionBytes - 1
	for i, ch := range slots[:255] {
		size := remaining / (255 - i)
		remaining -= size
		for left := size; left > 0; {
			count := min(left, MaxData)
			d.frame(Frame{DataCommand, ch.Channel, make([]byte, count)})
			left -= count
		}
	}
	d.frame(Frame{DataCommand, rpc.Channel, []byte("pc\n")})
	if !d.m.Channel(rpc.Channel).Active || d.m.Usage().Channels != 255 {
		t.Fatalf("full-slot isolation: %+v", d.m.Usage())
	}
}

func TestCapacitySmallRetainedRecipient(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ChannelBytes = 2 * MaxData
	cfg.SessionBytes = 3 * MaxData
	cfg.HighWater = 1024
	d := activeDriver(t, cfg, false)
	a, b, rpc := d.open(445), d.open(445), d.open(4700)
	d.frame(Frame{DataCommand, a.Channel, make([]byte, MaxData)})
	d.frame(Frame{DataCommand, a.Channel, make([]byte, MaxData)})
	d.frame(Frame{DataCommand, b.Channel, make([]byte, MaxData-1)})
	old := mustAction(t, d.frame(Frame{DataCommand, rpc.Channel, []byte("r")}), WriteLocal)
	out := d.frame(Frame{DataCommand, rpc.Channel, make([]byte, MaxData)})
	if close := mustAction(t, out, CloseLocal); close.Channel != a.Channel || !d.m.Channel(rpc.Channel).Active {
		t.Fatalf("tiny recipient selected instead of largest backlog: %+v", close)
	}
	w := mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 1}, old)), WriteLocal)
	if w.Channel != rpc.Channel || len(w.Data) != MaxData {
		t.Fatal("small recipient's retained bytes lost")
	}
}

func TestCapacityLargestRecipientCloses(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ChannelBytes = 3 * MaxData
	cfg.SessionBytes = 3 * MaxData
	cfg.HighWater = 1024
	d := activeDriver(t, cfg, false)
	a, b := d.open(445), d.open(4700)
	for i := 0; i < 2; i++ {
		d.frame(Frame{DataCommand, a.Channel, make([]byte, MaxData)})
	}
	d.frame(Frame{DataCommand, b.Channel, make([]byte, MaxData)})
	out := d.frame(Frame{DataCommand, a.Channel, []byte{1}})
	if close := mustAction(t, out, CloseLocal); close.Channel != a.Channel || !d.m.Channel(b.Channel).Active || d.m.Usage().RXBytes != MaxData {
		t.Fatalf("largest recipient policy: %+v usage=%+v", close, d.m.Usage())
	}
	if _, ok := find(out, WriteLocal); ok {
		t.Fatal("delivered incoming DATA after victim close")
	}
}
