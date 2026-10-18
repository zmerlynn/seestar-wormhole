package p2ptunnel

import (
	"slices"
)

// receiveQueue packs payload into chunks of at most MaxData. Only the first and
// last can be partial, bounding metadata even for one-byte DATA frames. Payload
// allocations have exact lengths; appending or partially draining copies at most
// one chunk rather than the whole backlog. The owner separately charges the
// returned front copy as its one outstanding local-write action.
type receiveQueue struct {
	chunks [][]byte
	size   int
}

func (q *receiveQueue) push(b []byte) {
	q.size += len(b)
	if last := len(q.chunks) - 1; last >= 0 && len(q.chunks[last]) < MaxData {
		old := q.chunks[last]
		n := min(MaxData-len(old), len(b))
		merged := make([]byte, len(old)+n)
		copy(merged, old)
		copy(merged[len(old):], b[:n])
		q.chunks[last] = merged
		b = b[n:]
	}
	for len(b) > 0 {
		n := min(len(b), MaxData)
		chunk := make([]byte, n)
		copy(chunk, b[:n])
		q.chunks = append(q.chunks, chunk)
		b = b[n:]
	}
}

func (q *receiveQueue) front() []byte {
	out := make([]byte, min(q.size, MaxData))
	offset := 0
	for _, b := range q.chunks {
		offset += copy(out[offset:], b)
		if offset == len(out) {
			break
		}
	}
	return out
}

func (q *receiveQueue) consume(n int) {
	q.size -= n
	removed := 0
	for n > 0 {
		b := q.chunks[removed]
		if n < len(b) {
			remaining := make([]byte, len(b)-n)
			copy(remaining, b[n:])
			q.chunks[removed] = remaining
			break
		}
		n -= len(b)
		removed++
	}
	q.chunks = slices.Delete(q.chunks, 0, removed)
	if q.size == 0 {
		q.chunks = nil
	}
}

func (q *receiveQueue) capacity() int {
	total := 0
	for _, b := range q.chunks {
		total += cap(b)
	}
	return total
}
