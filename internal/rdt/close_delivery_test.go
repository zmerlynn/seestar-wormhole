package rdt

import (
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func readyPair(t *testing.T) (*Machine, *Machine) {
	t.Helper()
	a := newMachine(t, nil)
	b := newMachine(t, func(c *Config) { c.LocalID = 9 })
	ready(t, a)
	b.Step(Event{Kind: Start}, 0)
	b.Step(Event{Kind: Receive, Packet: Packet{Type: Heartbeat, Version: 5, ID: 7}}, 0)
	out := b.Step(Event{Kind: Receive, Packet: Packet{Type: HeartbeatACK, Version: 5, ID: 9}}, 0)
	if !has(out, Ready) {
		t.Fatal("receiver did not establish", out)
	}
	return a, b
}

// Losing sender DATA/ABORT effects and delivering only its terminal QUIT must
// not make the peer observe EOF.
func TestQuitAfterFailedDeliveryIsNotEOF(t *testing.T) {
	for _, cause := range []string{"close-timeout", "abort"} {
		for _, prefix := range []string{"", "unread prefix"} {
			t.Run(cause+"/"+prefix, func(t *testing.T) {
				a, b := readyPair(t)
				if prefix != "" {
					for _, p := range packets(t, a.Step(Event{Kind: Write, Data: []byte(prefix)}, 0)) {
						b.Step(Event{Kind: Receive, Packet: p}, 0)
					}
				}
				// Drop DATA and all retransmissions, so the receiver has no visible gap.
				a.Step(Event{Kind: Write, Data: []byte("lost tail")}, 0)
				var out []Action
				var now core.Instant
				if cause == "close-timeout" {
					a.Step(Event{Kind: Close}, 0)
					a.Step(Event{Kind: Receive, Packet: Packet{Type: Heartbeat, Version: 5, ID: 9}}, core.Instant(4*time.Second))
					now = core.Instant(5 * time.Second)
					out = a.Step(Event{Kind: Tick}, now)
				} else {
					out = a.Step(Event{Kind: LocalAbort}, 0)
				}
				if !has(out, Failed) || has(out, Closed) || has(out, End) {
					t.Fatal("sender must fail without EOF", out)
				}
				quit, abort := 0, 0
				for _, p := range packets(t, out) {
					switch p.Type {
					case FIN:
						t.Fatal("sender certified undelivered data with FIN")
					case Abort:
						abort++ // Network drops ABORT.
					case Quit:
						quit++
						got := b.Step(Event{Kind: Receive, Packet: p}, now)
						if has(got, Closed) || has(got, End) || !has(got, Failed) {
							t.Fatalf("receiver Closed=%v End=%v Failed=%v; want failure without EOF", has(got, Closed), has(got, End), has(got, Failed))
						}
						if !has(got, CancelTimer) || b.Status() != Stopped || b.Usage() != (Usage{}) || b.Credit() != 0 {
							t.Fatal("failure did not release queues/timer", got, b.Usage())
						}
						if got := b.Step(Event{Kind: Read, Count: 8196}, now); has(got, End) || has(got, Bytes) {
							t.Fatal("failed read exposed bytes or EOF", got)
						}
					}
				}
				if quit != 1 || cause == "abort" && abort != 1 {
					t.Fatalf("sender emitted QUIT=%d ABORT=%d", quit, abort)
				}
			})
		}
	}
}

func TestPairedFINThenQuitPreservesOrderedEOF(t *testing.T) {
	a, b := readyPair(t)
	for _, p := range packets(t, a.Step(Event{Kind: Write, Data: []byte("tail")}, 0)) {
		b.Step(Event{Kind: Receive, Packet: p}, 0)
	}
	now := core.Instant(100 * time.Millisecond)
	for _, p := range packets(t, b.Step(Event{Kind: Tick}, now)) {
		a.Step(Event{Kind: Receive, Packet: p}, now)
	}
	fin := packets(t, a.Step(Event{Kind: Close}, now))
	if len(fin) != 1 || fin[0].Type != FIN {
		t.Fatal("drained sender must send FIN", fin)
	}
	out := b.Step(Event{Kind: Receive, Packet: fin[0]}, now)
	if !has(out, Closed) || has(out, Failed) || has(out, End) {
		t.Fatal("receiver must preserve unread tail", out)
	}
	var reply []Action
	for _, p := range packets(t, out) {
		reply = append(reply, a.Step(Event{Kind: Receive, Packet: p}, now)...)
	}
	if !has(reply, Closed) || !has(reply, End) || has(reply, Failed) {
		t.Fatal("sender FIN acknowledgement did not close cleanly", reply)
	}
	for _, p := range packets(t, reply) {
		if got := b.Step(Event{Kind: Receive, Packet: p}, now); len(got) != 0 {
			t.Fatal("late QUIT changed completed FIN result", got)
		}
	}
	out = b.Step(Event{Kind: Read, Count: 8196}, now)
	var data string
	for _, v := range out {
		if v.Kind == Bytes {
			data += string(v.Data)
		}
	}
	if data != "tail" || !has(out, End) || has(out, Failed) || b.Usage() != (Usage{}) {
		t.Fatal("final read lost ordered tail/EOF", out, b.Usage())
	}
	if out := b.Step(Event{Kind: Read, Count: 8196}, now); has(out, End) {
		t.Fatal("repeated EOF", out)
	}
}
