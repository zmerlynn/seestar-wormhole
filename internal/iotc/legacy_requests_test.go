package iotc

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

// Complete decoded send fixtures cover the legacy message catalog and
// session identity/data layouts. Flags include the sender endian
// bit; these expected bytes do not use Packet.Marshal or a builder.
func TestLiteralLegacyRequests(t *testing.T) {
	cases := []struct {
		name, hex string
		direct    bool
		event     EventKind
	}{
		{"direct data", "04021d0b0f00000007042100443300010c000000443322118877665517fefd", true, Write},
		{"relay data", "04021d0b0f00000004052400452300010c000000443322118877665517fefd", false, Write},
		{"direct alive", "04021d0b0800000027042100443300004433221188776655", true, Tick},
		{"relay alive", "04021d0b0800000024052400452300004433221188776655", false, Tick},
		{"direct close", "04021d0b0800000017042100443300004433221188776655", true, Cancel},
		{"relay close", "04021d0b0800000014052400452300004433221188776655", false, Cancel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := established(t, tc.direct)
			key := m.key
			out := m.Step(Event{Kind: tc.event, Attempt: 1, Data: []byte{0x17, 0xfe, 0xfd}}, core.Instant(2*time.Second))
			n := 0
			for _, a := range out {
				if a.Kind == Send {
					n++
					p, err := DecodeLegacy(a.Data, key)
					if err != nil {
						t.Fatal(err)
					}
					raw, err := p.Marshal()
					if err != nil || !bytes.Equal(raw, unhex(tc.hex)) {
						t.Fatalf("got %x want %s err=%v", raw, tc.hex, err)
					}
				}
			}
			if n == 0 {
				t.Fatal("missing send")
			}
		})
	}
	m, _ := New(config())
	m.add(endpoint{})
	m.hello(0)
	p, err := DecodeLegacy(m.out[0].Data, m.key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.Marshal()
	want := "04021d020800000003803f00000000000100000007000100"
	if err != nil || !bytes.Equal(raw, unhex(want)) {
		t.Fatalf("hello got %x want %s err=%v", raw, want, err)
	}
}
