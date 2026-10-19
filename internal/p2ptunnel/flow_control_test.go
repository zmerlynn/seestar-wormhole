package p2ptunnel

import (
	"bytes"
	"testing"
)

func TestHighWaterBoundary(t *testing.T) {
	for _, high := range []int{0, 1, 2} {
		cfg := DefaultConfig()
		cfg.HighWater = high
		m, err := New(cfg)
		if high < 2 {
			if err == nil {
				t.Fatalf("HighWater=%d accepted; integer low water must be positive", high)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = m
		d := activeDriver(t, cfg, false)
		ch := d.open(4700)
		w := mustAction(t, d.frame(Frame{DataCommand, ch.Channel, []byte{1, 2, 3}}), WriteLocal)
		if !d.m.Channel(ch.Channel).SentPause {
			t.Fatal("boundary holder did not pause")
		}
		d.step(local(Event{Kind: LocalWritten, Count: 3}, w))
		if d.m.Channel(ch.Channel).SentPause || !d.hasWire(ResumeCommand) {
			t.Fatal("HighWater=2 did not resume empty holder")
		}
	}
}

// A full TX queue holds 512 frames with 2048 control bytes. Hold DATA at the
// RDT handoff while pressure crosses both
// strict thresholds 1094 times: unsent PAUSE then RESUME must cancel.
func TestFlowChurnBehindBlockedData(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 100
	d := activeDriver(t, cfg, false)
	ch := d.open(445)
	d.frame(Frame{DataCommand, ch.Channel, []byte{1}})
	d.auto = false
	read := mustAction(t, d.step(local(Event{Kind: ReadReady}, ch)), ReadLocal)
	send := mustAction(t, d.step(local(Event{Kind: LocalData, Data: []byte{'D'}}, read)), Send)
	for i := 0; i < 1094; i++ {
		d.step(Event{Kind: ReceiveQueueSize, Count: 101})
		d.step(Event{Kind: ReceiveQueueSize, Count: 0})
	}
	if d.m.Status() != Active {
		t.Fatalf("control churn killed session: %v", d.m.Status())
	}
	out := d.step(Event{Kind: Sent, Token: send.Token, Count: len(send.Data)})
	if a, ok := find(out, Send); ok {
		t.Fatalf("canceled controls handed to RDT: %x", a.Data)
	}
	// The latest PAUSE must survive, once, when the DATA handoff finishes.
	d.step(Event{Kind: ReceiveQueueSize, Count: 101})
	a := mustAction(t, d.actions, Send)
	if !bytes.Equal(a.Data, []byte{5, ch.Channel, 0, 0}) {
		t.Fatalf("latest flow=%x", a.Data)
	}
	d.step(Event{Kind: ReceiveQueueSize, Count: 0})
	// Once handed off a control cannot be edited: finish it, then RESUME.
	out = d.step(Event{Kind: Sent, Token: a.Token, Count: 1})
	tail := mustAction(t, out, Send)
	if !bytes.Equal(tail.Data, []byte{ch.Channel, 0, 0}) {
		t.Fatalf("partial control tail=%x", tail.Data)
	}
	out = d.step(Event{Kind: Sent, Token: tail.Token, Count: 3})
	resume := mustAction(t, out, Send)
	if !bytes.Equal(resume.Data, []byte{6, ch.Channel, 0, 0}) {
		t.Fatalf("post-handoff resume=%x", resume.Data)
	}
	d.step(Event{Kind: Sent, Token: resume.Token, Count: 4})
}

func TestFlowZeroAcceptanceCancels(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 100
	d := activeDriver(t, cfg, false)
	ch := d.open(445)
	d.frame(Frame{DataCommand, ch.Channel, []byte{1}})
	d.auto = false
	pause := mustAction(t, d.step(Event{Kind: ReceiveQueueSize, Count: 101}), Send)
	d.step(Event{Kind: ReceiveQueueSize, Count: 0})
	d.step(Event{Kind: Sent, Token: pause.Token, Count: 0})
	out := d.step(Event{Kind: TransportWritable})
	if a, ok := find(out, Send); ok {
		t.Fatalf("zero-accepted canceled pause=%x", a.Data)
	}
	if d.m.Status() != Active {
		t.Fatal("session stopped")
	}
}

// Shared stream framing must survive a PAUSE becoming due between partial
// DATA completions, and WAKE must remain a single pending read opportunity.
func TestFlowWaitsForPartialData(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 100
	d := activeDriver(t, cfg, false)
	ch := d.open(445)
	d.frame(Frame{DataCommand, ch.Channel, []byte{1}})
	d.auto = false
	read := mustAction(t, d.step(local(Event{Kind: ReadReady}, ch)), ReadLocal)
	data := mustAction(t, d.step(local(Event{Kind: LocalData, Data: []byte{'D'}}, read)), Send)
	tail := mustAction(t, d.step(Event{Kind: Sent, Token: data.Token, Count: 2}), Send)
	d.step(Event{Kind: ReceiveQueueSize, Count: 101})
	out := d.step(Event{Kind: Sent, Token: tail.Token, Count: 1})
	next := mustAction(t, out, Send)
	if !bytes.Equal(next.Data, data.Data[3:]) {
		t.Fatalf("control inserted into DATA tail: %x", next.Data)
	}
	out = d.step(Event{Kind: Sent, Token: next.Token, Count: len(next.Data)})
	pause := mustAction(t, out, Send)
	if !bytes.Equal(pause.Data, []byte{5, ch.Channel, 0, 0}) {
		t.Fatalf("PAUSE after DATA=%x", pause.Data)
	}
	d.step(Event{Kind: Sent, Token: pause.Token, Count: 4})
	d.step(Event{Kind: TransportWritable})
	// Pause is now accepted; block a second DATA handoff and grant many WAKEs.
	read = mustAction(t, d.step(local(Event{Kind: ReadReady}, ch)), ReadLocal)
	data = mustAction(t, d.step(local(Event{Kind: LocalData, Data: []byte{'E'}}, read)), Send)
	for i := 0; i < 1094; i++ {
		d.m.control(WakeCommand, ch.Channel)
	}
	if u := d.m.Usage(); u.ControlFrames != 1 {
		t.Fatalf("pending WAKE charge=%+v", u)
	}
	wake := mustAction(t, d.step(Event{Kind: Sent, Token: data.Token, Count: len(data.Data)}), Send)
	if !bytes.Equal(wake.Data, []byte{7, ch.Channel, 0, 0}) {
		t.Fatalf("coalesced WAKE=%x", wake.Data)
	}
	d.step(Event{Kind: Sent, Token: wake.Token, Count: 0})
	wake = mustAction(t, d.step(Event{Kind: TransportWritable}), Send)
	if !bytes.Equal(wake.Data, []byte{7, ch.Channel, 0, 0}) {
		t.Fatalf("WAKE retry=%x", wake.Data)
	}
	out = d.step(Event{Kind: Sent, Token: wake.Token, Count: 4})
	if a, ok := find(out, Send); ok {
		t.Fatalf("duplicate WAKE=%x", a.Data)
	}
}
