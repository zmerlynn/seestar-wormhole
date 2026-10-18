package capturetrace

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/gopacket/gopacket/layers"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
)

func FuzzCapture(f *testing.F) {
	f.Add([]byte{})
	good := capture(f, layers.LinkTypeEthernet, encrypted(f, plainPacket(false)))
	f.Add(good)
	f.Add(good[:40])
	f.Add(capture(f, layers.LinkType(147)))
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := Decode(bytes.NewReader(data), strings.NewReader(syntheticKeylog()), Options{Attempt: 1, Limits: Limits{CaptureBytes: 4096, Packets: 8, Flows: 4, DecryptedRecords: 4, OutputBytes: 4096, KeylogBytes: 1024}})
		if len(got.JSONL) > 4096 {
			t.Fatal("output unbounded")
		}
		if err != nil && !got.Incomplete {
			t.Fatal("failure claimed complete")
		}
		if len(got.JSONL) > 0 {
			if _, e := tracerecord.Read(bytes.NewReader(got.JSONL)); e != nil {
				t.Fatal(fmt.Errorf("invalid projection: %w", e))
			}
		}
	})
}
