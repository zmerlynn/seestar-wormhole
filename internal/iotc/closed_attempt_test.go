package iotc

import "testing"

func TestNoSecretAdoptionAfterClose(t *testing.T) {
	m, _ := New(config())
	m.Step(Event{Kind: Start, Attempt: 1}, 0)
	m.Step(Event{Kind: Cancel, Attempt: 1}, 0)
	m.Step(Event{Kind: MaterialReady, Attempt: 1, Material: material()}, 0)
	if m.material != (Material{}) {
		t.Fatal("closed machine retained late master/EC material")
	}
}

func TestClosedIngressHasNoEffects(t *testing.T) {
	m, _ := New(config())
	m.Step(Event{Kind: Start, Attempt: 1}, 0)
	m.Step(Event{Kind: Cancel, Attempt: 1}, 0)
	for _, kind := range []EventKind{Write, Tick, Wire, AttemptFailed, EffectFailed, Connected, Resolved, MaterialReady, Derived, Encrypted, Decrypted} {
		if out := m.Step(Event{Kind: kind, Attempt: 1, Data: []byte{1}, Material: material()}, 0); len(out) != 0 {
			t.Fatalf("closed ingress %v emitted %v", kind, out)
		}
	}
	if m.material != (Material{}) || m.cache.Count != 0 || m.count != 0 || m.pending != ([64]operation{}) {
		t.Fatal("closed machine retained attempt resources")
	}
}
