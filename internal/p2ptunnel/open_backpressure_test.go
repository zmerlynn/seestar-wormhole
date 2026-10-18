package p2ptunnel

import (
	"bytes"
	"testing"
	"time"
)

func TestOpenOutputBackpressure(t *testing.T) {
	for _, verify := range []bool{false, true} {
		for _, bytePressure := range []bool{false, true} {
			d := activeDriver(t, DefaultConfig(), verify)
			// Occupy the bounded frame slots without pretending the transport failed.
			// Fullness must leave the session and pending verifier unchanged.
			fill := func() {
				d.m.sendBlocked = true
				if bytePressure {
					d.m.cfg.TXBytes = 2 * MaxInput
					for range 2 {
						d.m.queue(Frame{Command: DataCommand, Payload: bytes.Repeat([]byte{42}, MaxData)}, Established)
					}
				} else {
					for len(d.m.tx) < d.m.cfg.TXFrames {
						d.m.queue(Frame{Command: EndCommand}, Established)
					}
				}
			}
			fill()
			a := mustAction(t, d.step(Event{Kind: Open, Request: 24, Port: 4700}), OpenFailed)
			if !a.Temporary || d.m.Status() != Active || d.m.pending != nil || d.m.Usage().Channels != 0 {
				t.Fatalf("verify=%v action=%+v status=%v", verify, a, d.m.Status())
			}
			d.m.tx = nil
			d.m.txBytes = 0
			d.m.sendBlocked = false
			a = mustAction(t, d.step(Event{Kind: Open, Request: 24, Port: 4700}), map[bool]ActionKind{false: Opened, true: Send}[verify])
			if verify {
				// Congestion can also occur after verification but before SETUP.
				fill()
				a = mustAction(t, d.step(Event{Kind: Stream, Data: []byte{13, 0, 6, 0, 1, 0, 0x5c, 0x12, 1, 0}}), OpenFailed)
				if !a.Temporary || d.m.Status() != Active {
					t.Fatalf("SETUP congestion=%+v status=%v", a, d.m.Status())
				}
			}
		}
	}
}

func TestCanceledOpenRetiresCorrelation(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), true)
	d.step(Event{Kind: Open, Request: 24, Port: 4700})
	d.step(Event{Kind: CancelOpen, Request: 999}) // Another caller cannot cancel it.
	if d.m.pending == nil {
		t.Fatal("unrelated cancellation retired verification")
	}
	d.step(Event{Kind: CancelOpen, Request: 24})
	d.step(Event{Kind: Open, Request: 25, Port: 4700})
	for _, b := range [][]byte{
		{13, 0, 6, 0, 1, 0, 0x5c, 0x12, 1, 0}, // retired correlation
		{13, 0, 6, 0, 3, 0, 0x5c, 0x12, 1, 0}, // unrelated correlation
	} {
		if a := d.step(Event{Kind: Stream, Data: b}); len(a) != 0 {
			t.Fatalf("unmatched reply actions=%+v", a)
		}
	}
	d.now = d.now.Add(time.Second)
	a := mustAction(t, d.step(Event{Kind: Stream, Data: []byte{13, 0, 6, 0, 2, 0, 0x5c, 0x12, 1, 0}}), Opened)
	if a.Request != 25 || d.m.Usage().Channels != 1 {
		t.Fatalf("current reply=%+v usage=%+v", a, d.m.Usage())
	}
}

func TestVerificationRefusalNeverTemporary(t *testing.T) {
	for _, tail := range [][]byte{{0x5c, 0x12, 0, 0}, {0x5d, 0x12, 1, 0}} {
		d := activeDriver(t, DefaultConfig(), true)
		d.step(Event{Kind: Open, Request: 24, Port: 4700})
		b := append([]byte{13, 0, 6, 0, 1, 0}, tail...)
		a := mustAction(t, d.step(Event{Kind: Stream, Data: b}), OpenFailed)
		if a.Temporary || d.m.Usage().Channels != 0 {
			t.Fatalf("refusal=%+v", a)
		}
	}
}
