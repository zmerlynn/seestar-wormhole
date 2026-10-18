package core

import "testing"

func TestBudget(t *testing.T) {
	b := Budget{MaxBytes: 10, MaxCount: 2}
	if !b.Reserve(10, 1) || b.Reserve(1, 0) || b.Reserve(0, 2) || b.Reserve(-1, 0) {
		t.Fatal(b)
	}
	b.Release(10, 1)
	if b.Bytes != 0 || b.Count != 0 {
		t.Fatal(b)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("invalid release accepted")
		}
	}()
	b.Release(1, 0)
}
