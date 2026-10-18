package p2ptunnel

import (
	"bytes"
	"testing"
)

// Requested successor is generated after selection of its predecessor. Keep
// every other wire slot eligible and FIFO backlogged using only valid events.
func TestWakeServiceIncludesPredecessor(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 100
	d := activeDriver(t, cfg, false)
	var ch [256]Action
	for i := range ch {
		ch[i] = d.open(445)
	}
	d.auto = false
	out := d.frame(Frame{DataCommand, ch[0].Channel, bytes.Repeat([]byte{'a'}, MaxData)})
	send := mustAction(t, out, Send)
	w := mustAction(t, out, WriteLocal)
	for i := 1; i < 256; i++ {
		d.frame(Frame{DataCommand, ch[i].Channel, bytes.Repeat([]byte{'b'}, 101)})
	}
	for i := 0; i < 2; i++ {
		d.frame(Frame{DataCommand, ch[0].Channel, bytes.Repeat([]byte{'a'}, MaxData)})
	}
	w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: MaxData}, w)), WriteLocal)
	d.step(local(Event{Kind: LocalWritten, Count: 5}, w))
	for i := range ch {
		r := mustAction(t, d.step(local(Event{Kind: ReadReady}, ch[i])), ReadLocal)
		d.step(local(Event{Kind: LocalData, Data: []byte{'D'}}, r))
	}
	for n := 1; n <= 514; n++ {
		if bytes.Equal(send.Data, []byte{7, ch[0].Channel, 0, 0}) {
			t.Logf("WAKE completed frame=%d including outstanding PAUSE", n)
			if n != 513 {
				t.Fatalf("WAKE service frame=%d; want exactly 513 including outstanding PAUSE", n)
			}
			return
		}
		send = mustAction(t, d.step(Event{Kind: Sent, Token: send.Token, Count: len(send.Data)}), Send)
	}
	t.Fatal("WAKE not served")
}

// Unlike a successor behind PAUSE, this second WAKE is already eligible
// throughout the outstanding first WAKE: PAUSE was fully accepted earlier.
func TestEligibleWakeServiceIncludesOutstanding(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 100
	d := activeDriver(t, cfg, false)
	var ch [256]Action
	for i := range ch {
		ch[i] = d.open(445)
	}
	var w Action
	for i := 0; i < 5; i++ {
		out := d.frame(Frame{DataCommand, ch[0].Channel, bytes.Repeat([]byte{'a'}, MaxData)})
		if a, ok := find(out, WriteLocal); ok {
			w = a
		}
	}
	d.auto = false
	w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: MaxData}, w)), WriteLocal)
	out := d.step(local(Event{Kind: LocalWritten, Count: 5}, w))
	send := mustAction(t, out, Send)
	w = mustAction(t, out, WriteLocal)
	if !bytes.Equal(send.Data, []byte{7, ch[0].Channel, 0, 0}) {
		t.Fatalf("setup WAKE=%x", send.Data)
	}
	for i := 1; i < 256; i++ {
		d.frame(Frame{DataCommand, ch[i].Channel, bytes.Repeat([]byte{'b'}, 101)})
	}
	// Two valid completions generate a new opportunity (8187 + 10 = 8197).
	w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: len(w.Data)}, w)), WriteLocal)
	d.step(local(Event{Kind: LocalWritten, Count: 10}, w))
	for i := range ch {
		r := mustAction(t, d.step(local(Event{Kind: ReadReady}, ch[i])), ReadLocal)
		d.step(local(Event{Kind: LocalData, Data: []byte{'D'}}, r))
	}
	for n := 1; n <= 514; n++ {
		if n > 1 && bytes.Equal(send.Data, []byte{7, ch[0].Channel, 0, 0}) {
			t.Logf("second eligible WAKE completed frame=%d including outstanding WAKE", n)
			if n != 513 {
				t.Fatalf("eligible WAKE service frame=%d; want exactly 513 including outstanding WAKE", n)
			}
			return
		}
		send = mustAction(t, d.step(Event{Kind: Sent, Token: send.Token, Count: len(send.Data)}), Send)
	}
	t.Fatal("eligible WAKE not served")
}
