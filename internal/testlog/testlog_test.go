package testlog

import (
	"fmt"
	"sync"
	"testing"
)

// Writes from many goroutines all land; String keeps them and Take
// empties the buffer.
func TestBuffer(t *testing.T) {
	var b Buffer
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() { fmt.Fprintf(&b, "%d\n", i) })
	}
	wg.Wait()
	if s := b.String(); len(s) != 16 || b.String() != s {
		t.Errorf("String = %q, want eight lines, unchanged by reading", s)
	}
	if s := b.Take(); len(s) != 16 {
		t.Errorf("Take = %q, want eight lines", s)
	}
	b.Write([]byte("next\n"))
	if s := b.Take(); s != "next\n" {
		t.Errorf("Take = %q, want only what followed the last Take", s)
	}
	if s := b.String(); s != "" {
		t.Errorf("String after Take = %q, want empty", s)
	}
}
