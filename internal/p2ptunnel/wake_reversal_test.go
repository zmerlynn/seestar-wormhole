package p2ptunnel

import (
	"bytes"
	"testing"
)

// A zero-accepted WAKE belongs to an earlier pause epoch, even if pressure
// falls below low water and returns above high water before its completion.
func TestZeroWakeSupersededByResumeRePause(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 100
	d := activeDriver(t, cfg, false)
	ch := d.open(445)
	var w Action
	for i := 0; i < 3; i++ {
		out := d.frame(Frame{DataCommand, ch.Channel, bytes.Repeat([]byte{'a'}, MaxData)})
		if a, ok := find(out, WriteLocal); ok {
			w = a
		}
	}
	d.auto = false
	w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: MaxData}, w)), WriteLocal)
	out := d.step(local(Event{Kind: LocalWritten, Count: 5}, w))
	wake := mustAction(t, out, Send)
	w = mustAction(t, out, WriteLocal)
	if !bytes.Equal(wake.Data, []byte{7, ch.Channel, 0, 0}) {
		t.Fatalf("setup WAKE=%x", wake.Data)
	}
	w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: len(w.Data)}, w)), WriteLocal)
	d.step(local(Event{Kind: LocalWritten, Count: len(w.Data)}, w))
	if d.m.Channel(ch.Channel).SentPause {
		t.Fatal("not resumed")
	}
	d.frame(Frame{DataCommand, ch.Channel, bytes.Repeat([]byte{'b'}, 101)})
	if !d.m.Channel(ch.Channel).SentPause {
		t.Fatal("not paused again")
	}
	d.step(Event{Kind: Sent, Token: wake.Token, Count: 0})
	out = d.step(Event{Kind: TransportWritable})
	if send, ok := find(out, Send); ok {
		t.Fatalf("superseded zero-accepted WAKE resurrected after RESUME/PAUSE: %x", send.Data)
	}
}

// Superseding a retry must preserve a partial frame and independently generated
// WAKE opportunities. Both cases use the same RESUME -> PAUSE reversal.
func TestWakeReversalPreservesPartialAndNewOpportunity(t *testing.T) {
	for _, newer := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "newer"}[newer], func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.HighWater = 100
			d := activeDriver(t, cfg, false)
			ch := d.open(445)
			var w Action
			for i := 0; i < 3; i++ {
				out := d.frame(Frame{DataCommand, ch.Channel, bytes.Repeat([]byte{'a'}, MaxData)})
				if a, ok := find(out, WriteLocal); ok {
					w = a
				}
			}
			d.auto = false
			w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: MaxData}, w)), WriteLocal)
			out := d.step(local(Event{Kind: LocalWritten, Count: 5}, w))
			wake := mustAction(t, out, Send)
			w = mustAction(t, out, WriteLocal)
			if !bytes.Equal(wake.Data, []byte{7, ch.Channel, 0, 0}) {
				t.Fatalf("setup WAKE=%x", wake.Data)
			}
			w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: len(w.Data)}, w)), WriteLocal)
			d.step(local(Event{Kind: LocalWritten, Count: len(w.Data)}, w))
			// Queue enough new pressure to generate a fresh opportunity if requested.
			for i := 0; i < 3; i++ {
				out = d.frame(Frame{DataCommand, ch.Channel, bytes.Repeat([]byte{'b'}, MaxData)})
				if a, ok := find(out, WriteLocal); ok {
					w = a
				}
			}
			if !d.m.Channel(ch.Channel).SentPause {
				t.Fatal("not paused again")
			}
			if newer {
				w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: MaxData}, w)), WriteLocal)
				d.step(local(Event{Kind: LocalWritten, Count: 5}, w))
				d.step(Event{Kind: Sent, Token: wake.Token, Count: 0})
				send := mustAction(t, d.step(Event{Kind: TransportWritable}), Send)
				if !bytes.Equal(send.Data, []byte{7, ch.Channel, 0, 0}) {
					t.Fatalf("new WAKE=%x", send.Data)
				}
				out = d.step(Event{Kind: Sent, Token: send.Token, Count: len(send.Data)})
			} else {
				send := mustAction(t, d.step(Event{Kind: Sent, Token: wake.Token, Count: 1}), Send)
				d.step(Event{Kind: Sent, Token: send.Token, Count: 0})
				send = mustAction(t, d.step(Event{Kind: TransportWritable}), Send)
				if !bytes.Equal(send.Data, []byte{ch.Channel, 0, 0}) {
					t.Fatalf("immutable WAKE tail=%x", send.Data)
				}
				out = d.step(Event{Kind: Sent, Token: send.Token, Count: len(send.Data)})
			}
			if send, ok := find(out, Send); ok {
				t.Fatalf("duplicate superseded opportunity=%x", send.Data)
			}
		})
	}
}
