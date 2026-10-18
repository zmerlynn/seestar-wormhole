package proxy

import (
	"bufio"
	"context"
	"crypto/sha256"
	"io"
	"runtime"
	"testing"
	"testing/synctest"
	"time"
)

// Carry the byte volume and socket pacing through real IOTC/DTLS/RDT and
// the internal stream. The application payload is synthetic; the paired tunnel
// regression separately delays controls until after a saved payload prefix.
func TestComposedSlowReader(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		s, stacks, cloud := workloadSession(t, ctx, func(p *simulatedWire) { p.responseBytes = 256 << 10 })
		defer s.Close()
		c, err := s.DialPort(ctx, 445)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		sibling, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer sibling.Close()
		x := <-stacks
		const volume = 16594560
		// Socket reads are at most 16KiB, followed by byte-proportional pacing.
		reader := bufio.NewReaderSize(&pacedReader{reader: c}, 16<<10)
		var digests [2][32]byte
		var baseline uint64
		for pass := 0; pass < 2; pass++ {
			h := sha256.New()
			delivered := 0
			for delivered < volume {
				if _, err := c.Write([]byte(rpcLine(99, 0))); err != nil {
					t.Fatal(err)
				}
				line, err := reader.ReadBytes('\n')
				if err != nil {
					t.Fatalf("pass %d after %d bytes: %v", pass, delivered, err)
				}
				if err := checkResponse(string(line), 99); err != nil {
					t.Fatal(err)
				}
				n := min(len(line), volume-delivered)
				h.Write(line[:n])
				delivered += n
				if err := exchange(sibling.(*stream), delivered, 0); err != nil {
					t.Fatal(err)
				}
				inspectStack(t, x)
				// Compare live heap after the pipeline has warmed up, across both passes.
				if delivered >= 1<<20 {
					runtime.GC()
					var mem runtime.MemStats
					runtime.ReadMemStats(&mem)
					if baseline == 0 {
						baseline = mem.HeapAlloc
					}
					if mem.HeapAlloc > baseline+2<<20 {
						t.Fatalf("growing live heap: baseline=%d current=%d", baseline, mem.HeapAlloc)
					}
				}
			}
			copy(digests[pass][:], h.Sum(nil))
		}
		if digests[0] != digests[1] {
			t.Fatal("two read hashes differ")
		}
		if cloud.allocs.Load() != 1 {
			t.Fatal("slow reader replaced session")
		}
		t.Logf("two %d-byte passes at 204800 B/s; sibling live; heap baseline=%d", volume, baseline)
	})
}

type pacedReader struct{ reader io.Reader }

func (r *pacedReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p[:min(len(p), 16<<10)])
	time.Sleep(time.Duration(n) * time.Second / 204800) // virtual clock inside synctest
	return n, err
}
