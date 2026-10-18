package rdt

import "testing"

func TestBoundedSendCompletion(t *testing.T) {
	c := DefaultConfig()
	c.SendCapacity = 1
	m, _ := New(c)
	m.status = Established
	m.remote = 2
	m.heartbeatAt = 1 << 60
	m.watchdog = 120
	sends := 0
	for _, a := range m.Step(Event{Kind: Write, Data: make([]byte, 8196)}, 0) {
		if a.Kind == Send {
			sends++
		}
	}
	if sends != 1 {
		t.Fatalf("sends %d, want 1", sends)
	}
	for i := 0; i < 6; i++ {
		sends = 0
		for _, a := range m.Step(Event{Kind: SendComplete}, 0) {
			if a.Kind == Send {
				sends++
			}
		}
		if sends != 1 {
			t.Fatalf("completion %d sends %d", i, sends)
		}
	}
}
