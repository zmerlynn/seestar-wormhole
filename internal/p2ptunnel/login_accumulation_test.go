package p2ptunnel

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// Accumulate exactly one bounded frame before deciding; native short-delivery
// success is not required.
func TestLoginFrameAccumulation(t *testing.T) {
	for _, wire := range []string{
		"08000000", "08000100ff", "08000200ffff", "08000300ffffff",
		"08000400ffffffff", "08000500ffffffff01", "08000600ffffffff0101",
		"08000700ffffffff010101", "08000800ffffffff02000300",
		"08000900ffffffff02000300aa",
	} {
		t.Run(wire, func(t *testing.T) {
			raw := unhex(t, wire)
			for split := 1; split < len(raw); split++ {
				d := newDriver(t, DefaultConfig())
				d.step(Event{Kind: Start})
				a := d.step(Event{Kind: Stream, Data: raw[:split]})
				if _, ready := find(a, SessionReady); ready || d.m.Status() != LoggingIn {
					t.Fatalf("split %d: incomplete frame status=%v actions=%+v", split, d.m.Status(), a)
				}
				a = d.step(Event{Kind: Stream, Data: raw[split:]})
				if _, ready := find(a, SessionReady); !ready || d.m.Status() != Active {
					t.Fatalf("split %d: complete PASS status=%v actions=%+v", split, d.m.Status(), a)
				}
				d.step(Event{Kind: Open, Port: 4700, Request: 1})
				if got := d.hasWire(PortRequest); got != (len(raw) >= 12) {
					t.Fatalf("split %d: port verification request=%v, L=%d", split, got, len(raw)-4)
				}
			}
		})
	}
}

// One delivery contains PASS and an unknown established frame. Leave
// the next frame for the established dispatcher, which skips it.
func TestLoginCoalescedFrames(t *testing.T) {
	raw := unhex(t, "0800080000000000000000000a010300616263")
	var decoder Decoder
	used, frame, err := decoder.Feed(raw, LoginResponse)
	if err != nil || frame == nil || used != 12 || !bytes.Equal(raw[used:], unhex(t, "0a010300616263")) {
		t.Fatalf("login read beyond frame: used=%d frame=%+v err=%v", used, frame, err)
	}
	d := newDriver(t, DefaultConfig())
	d.step(Event{Kind: Start})
	a := d.step(Event{Kind: Stream, Data: raw})
	if d.m.Status() != Active || d.m.Usage().ParserBytes != 0 {
		t.Fatalf("next frame was lost or misread: status=%v usage=%+v actions=%+v", d.m.Status(), d.m.Usage(), a)
	}
}

func TestLoginShortResultTeardown(t *testing.T) {
	for _, prefix := range []string{"08", "0800", "080008", "080008000000000001"} {
		for _, terminal := range []EventKind{Tick, TransportFailed} {
			t.Run(fmt.Sprintf("%s/%d", prefix, terminal), func(t *testing.T) {
				d := newDriver(t, DefaultConfig())
				d.step(Event{Kind: Start})
				d.step(Event{Kind: Stream, Data: unhex(t, prefix)})
				d.now = core.Instant(30 * time.Second)
				a := d.step(Event{Kind: terminal})
				if _, ready := find(a, SessionReady); ready || d.m.Status() != Stopped {
					t.Fatalf("short result passed: status=%v actions=%+v", d.m.Status(), a)
				}
			})
		}
	}
	for _, wire := range []string{"090008000000000001000100", "0a000000", "08000120"} {
		d := newDriver(t, DefaultConfig())
		d.step(Event{Kind: Start})
		d.step(Event{Kind: Stream, Data: unhex(t, wire)})
		if d.m.Status() != Stopped {
			t.Fatalf("unsafe result %s passed", wire)
		}
	}
}

// Enable port verification from nonzero byte 4 only in a full result.
func TestLoginResultCapabilities(t *testing.T) {
	for _, tc := range []struct {
		wire string
		port bool
	}{
		{"080008000000000000ff0000", false},
		{"080008000000000002000300", true},
		{"080007000000000002ff03", false},
	} {
		d := newDriver(t, DefaultConfig())
		d.step(Event{Kind: Start})
		d.step(Event{Kind: Stream, Data: unhex(t, tc.wire)})
		if d.m.Status() != Active {
			t.Fatalf("%s: status=%v, want Active", tc.wire, d.m.Status())
		}
		d.step(Event{Kind: Open, Port: 4700, Request: 1})
		if got := d.hasWire(PortRequest); got != tc.port {
			t.Fatalf("%s: port verification request=%v, want %v", tc.wire, got, tc.port)
		}
	}
}
