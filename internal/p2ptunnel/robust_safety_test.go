package p2ptunnel

import (
	"testing"
)

func TestCompatibilitySafetyBoundaries(t *testing.T) {
	// All are complete or deliberately truncated independent literals.
	for _, tt := range []struct {
		wire  string
		phase Phase
	}{
		{"0800080000000000", LoginResponse}, // L=8, only result present
		{"0a000000", LoginResponse},
		{"010711000200125c7f000001000000000000000000", Established},
		{"03070100ff", Established}, {"0d07050034125c1201", Established},
	} {
		if _, err := Decode(unhex(t, tt.wire), tt.phase); err == nil {
			t.Fatalf("unsafe input accepted: %s phase=%d", tt.wire, tt.phase)
		}
	}
	for _, wire := range []string{"090008000000000000000000", "0a000000"} {
		d := newDriver(t, DefaultConfig())
		d.step(Event{Kind: Start})
		d.step(Event{Kind: Stream, Data: unhex(t, wire)})
		if d.m.Status() != Stopped {
			t.Fatalf("refusal/phase guard lost: %s", wire)
		}
	}
	for _, cmd := range []byte{10, 1, 4} {
		var decoder Decoder
		_, _, err := decoder.Feed([]byte{cmd, 7, 1, 32}, Established) // 8193 > receive cap
		if err == nil || decoder.Buffered() != HeaderSize {
			t.Fatalf("cmd %d allocated oversized body", cmd)
		}
	}
	// Extended SETUP still requires a bounded, terminated service name.
	b := unhex(t, auditTailSetup)
	for i := 20; i < 53; i++ {
		b[i] = 'x'
	}
	if _, err := Decode(b, Established); err == nil {
		t.Fatal("unterminated service accepted")
	}
}
