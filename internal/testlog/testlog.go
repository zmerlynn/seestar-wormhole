// Package testlog is a log sink for tests: a buffer a logger can write from
// other goroutines while the test reads it.
package testlog

import (
	"bytes"
	"sync"
)

// Buffer is a bytes.Buffer whose writes and reads are safe to race. Its
// zero value is empty and ready to use.
type Buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

// String returns everything written so far.
func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// Take returns what was written since the last Take, and empties the buffer.
func (b *Buffer) Take() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.b.String()
	b.b.Reset()
	return s
}
