package rdt

import (
	"reflect"
	"testing"
)

func TestUnknownDiagnostic(t *testing.T) {
	m := newMachine(t, nil)
	ready(t, m)
	// Independent unknown literal: nonzero sequence/ID/reserved fields and
	// synthetic secret "rdt-secret" must never leave the core in a diagnostic.
	p, err := Decode(unhex(t, "5a97c2f144050a0078563412efcdab89071134127264742d736563726574"))
	if err != nil {
		t.Fatal(err)
	}
	a := m.Step(Event{Kind: Receive, Packet: p}, 0)
	want := []Action{{Kind: ReceiveOddity, Type: 0x44}}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("diagnostic contains fields other than kind/type: got %+v; want %+v", a, want)
	}
}
