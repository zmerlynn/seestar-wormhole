package edge

import (
	"encoding/binary"
	"io"
)

// HelloSerialStart draws an independent IOTC hello serial origin per attempt.
// The Generate executor must call this with crypto/rand.Reader in production
// and supply the result in MaterialReady. Tests may inject a deterministic RNG.
// Hello serials increment as LE32 values with no narrower observed range;
// zero is excluded only because the machine reserves it for unissued requests.
func HelloSerialStart(rng io.Reader) (uint32, error) {
	var b [4]byte
	for {
		if _, err := io.ReadFull(rng, b[:]); err != nil {
			return 0, err
		}
		if serial := binary.LittleEndian.Uint32(b[:]); serial != 0 {
			return serial, nil
		}
	}
}
