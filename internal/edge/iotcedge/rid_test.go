package iotcedge

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

// Only RID draws are scripted; other cryptographic material uses the original
// RNG. The child process isolates both rand.Reader and process-lifetime history,
// including when the parent suite runs with -count=2 or the race detector.
type ridReader struct {
	other io.Reader
	rids  *bytes.Reader
}

func (r ridReader) Read(b []byte) (int, error) {
	if len(b) == 8 {
		return r.rids.Read(b)
	}
	return r.other.Read(b)
}

func TestRIDProcessNonReuse(t *testing.T) {
	if os.Getenv("WORMHOLE_RID_TEST_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRIDProcessNonReuse$", "-test.v")
		cmd.Env = append(os.Environ(), "WORMHOLE_RID_TEST_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("RID process policy: %v\n%s", err, out)
		}
		return
	}
	// A, A/B, A/B/C: collide with every earlier attempt, across a new worker
	// and Clear. Reusing a numeric local Attempt must not reset wire history.
	const a, b, c = "4433221188776655", "aabbccddeeff0011", "0123456789abcdef"
	raw, err := hex.DecodeString(a + a + b + a + b + c)
	if err != nil {
		t.Fatal(err)
	}
	original := rand.Reader
	rand.Reader = ridReader{other: original, rids: bytes.NewReader(raw)}
	t.Cleanup(func() { rand.Reader = original })
	var first, second IOTCCrypto
	for i, tc := range []struct {
		worker *IOTCCrypto
		want   string
	}{{&first, a}, {&second, b}, {&first, c}} {
		e, err := tc.worker.Execute(iotc.Action{Kind: iotc.Generate, Attempt: 1})
		if err != nil || e.Kind != iotc.MaterialReady {
			t.Fatalf("attempt %d: kind=%v err=%v", i, e.Kind, err)
		}
		if got := hex.EncodeToString(e.Material.RID[:]); got != tc.want {
			t.Fatalf("attempt %d RID=%s, want %s after collision rejection", i, got, tc.want)
		}
		tc.worker.Clear()
	}
}

func TestRIDHistoryRejectsZeroAndFailsClosed(t *testing.T) {
	var history ridHistory
	const literal = "4433221188776655"
	raw, _ := hex.DecodeString("0000000000000000" + literal)
	rid, err := history.generate(bytes.NewReader(raw))
	if err != nil || hex.EncodeToString(rid[:]) != literal {
		t.Fatalf("RID=%x err=%v, want nonzero literal", rid, err)
	}
	for _, tc := range []struct {
		name string
		rng  io.Reader
	}{
		{"short entropy", bytes.NewReader([]byte{1, 2, 3})},
		{"zero budget", bytes.NewReader(make([]byte, 32*8))},
		{"collision budget", bytes.NewReader(bytes.Repeat(rid[:], 32))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := history.generate(tc.rng)
			if got != [8]byte{} || !errors.Is(err, ErrIOTCCrypto) {
				t.Fatalf("RID=%x err=%v, want zero and sanitized crypto failure", got, err)
			}
		})
	}
}

func TestRIDHistoryConcurrentReservation(t *testing.T) {
	var history ridHistory
	type result struct {
		rid [8]byte
		err error
	}
	const workers = 16
	results := make(chan result, workers)
	for i := range workers {
		go func() {
			// Every worker first proposes the same literal; fallbacks differ.
			raw, _ := hex.DecodeString("4433221188776655aabbccddeeff0011")
			raw[15] = byte(i + 1)
			rid, err := history.generate(bytes.NewReader(raw))
			results <- result{rid, err}
		}()
	}
	seen := make(map[[8]byte]bool)
	for range workers {
		r := <-results
		if r.err != nil || r.rid == [8]byte{} || seen[r.rid] {
			t.Errorf("concurrent RID=%x err=%v, duplicate=%v", r.rid, r.err, seen[r.rid])
		}
		seen[r.rid] = true
	}
}
