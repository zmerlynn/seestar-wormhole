package p2ptunnel

import (
	"testing"
	"time"
)

// Literal peer bytes isolate the outage symptom from network scheduling and
// output-credit pressure. The successful response arrives after five seconds.
func TestOutageVerificationDiagnosis(t *testing.T) {
	d := newDriver(t, DefaultConfig())
	d.step(Event{Kind: Start})
	d.step(Event{Kind: Stream, Data: []byte{8, 0, 8, 0, 0, 0, 0, 0, 1, 0, 0, 0}})
	d.step(Event{Kind: Open, Request: 24, Port: 4700})
	d.now = d.now.Add(5 * time.Second)
	a := mustAction(t, d.step(Event{Kind: Tick}), OpenFailed)
	if a.Reason != "verification timeout" || d.m.Status() != Active || d.m.Usage().TXBytes != 0 {
		t.Fatalf("expiry=%+v status=%v usage=%+v", a, d.m.Status(), d.m.Usage())
	}
	t.Logf("cause=%q status=%v usage=%+v", a.Reason, d.m.Status(), d.m.Usage())
	// A delayed reply to the expired transaction cannot authorize SETUP.
	d.now = d.now.Add(time.Second)
	if a := d.step(Event{Kind: Stream, Data: []byte{13, 0, 6, 0, 1, 0, 0x5c, 0x12, 1, 0}}); len(a) != 0 {
		t.Fatalf("expired response emitted actions: %+v", a)
	}
}
