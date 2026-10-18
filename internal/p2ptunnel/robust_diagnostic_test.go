package p2ptunnel

import (
	"reflect"
	"testing"
)

func TestUnknownDiagnostic(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	// Independent unknown literal: channel 7 and synthetic secret
	// "tunnel-secret" must never leave the core in a diagnostic.
	a := d.step(Event{Kind: Stream, Data: unhex(t, "0a070d0074756e6e656c2d736563726574")})
	want := []Action{{Kind: ReceiveOddity, Command: 10}}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("diagnostic contains fields other than kind/command: got %+v; want %+v", a, want)
	}
}
