package proxy

import (
	"bytes"
	"sync"
)

// The live timer owner can become runnable between virtual-time barriers.
// Snapshots must synchronize with the handler's concurrent writes.
type logBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *logBuffer) take() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := bytes.Clone(b.buffer.Bytes())
	b.buffer.Reset()
	return p
}
