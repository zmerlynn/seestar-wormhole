package session

import "testing"

func TestSessionDiscardCounters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []Event
	}{
		{"stale_attempt", []Event{{Kind: Progress, Attempt: 999}}},
		{"wrong_phase", []Event{{Kind: Progress}}},
		{"unknown_client", []Event{{Kind: ClientGone, Client: 7}}},
		{"repeated_start", []Event{{Kind: Start}, {Kind: Start}}},
		{"duplicate_client", []Event{{Kind: Start}, {Kind: Client, Client: 7}, {Kind: Client, Client: 7}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := New(DefaultConfig(), fixedRNG(1))
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range tc.events {
				m.Step(e, 0)
			}
			if n := m.Snapshot().Discards; n != 1 {
				t.Fatalf("R097 count = %d, want 1", n)
			}
		})
	}
}
