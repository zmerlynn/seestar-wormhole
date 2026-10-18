package edge

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestHelloSerialStart(t *testing.T) {
	// Two consecutive attempts consume independent input, including high bits.
	// Zero is rejected, with no truncation, sign mask or counter fallback.
	rng := bytes.NewReader([]byte{0, 0, 0, 0, 0xfe, 0xdc, 0xba, 0x98, 1, 2, 3, 4})
	for _, want := range []uint32{0x98badcfe, 0x04030201} {
		got, err := HelloSerialStart(rng)
		if err != nil || got != want {
			t.Fatalf("serial=%08x err=%v, want %08x", got, err, want)
		}
	}
	if got, err := HelloSerialStart(rng); got != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("exhausted RNG: serial=%x err=%v, want 0, EOF", got, err)
	}
	if _, err := HelloSerialStart(bytes.NewReader([]byte{1, 2, 3})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("short RNG err=%v, want unexpected EOF", err)
	}
}
