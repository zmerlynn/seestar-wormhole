package ui

import "testing"

func TestCompletedKeychainCallReleasesGate(t *testing.T) {
	c := &credentialStore{}
	for range 1000 {
		if _, err := keychainCall(t.Context(), c, func() (string, error) { return "", nil }); err != nil {
			t.Fatal(err)
		}
		if c.state("").Busy {
			t.Fatal("completed native request still owns the gate")
		}
	}
}
