package p2ptunnel

import (
	"bytes"
	"testing"
)

func TestC16UnknownStreamSplits(t *testing.T) {
	b := unhex(t, auditUnknownStream)
	for split := 0; split <= len(b); split++ {
		d := activeDriver(t, DefaultConfig(), false)
		// Exercise the literal's channel, with an independent live sibling.
		for i := 1; i <= 7; i++ {
			d.open(uint16(4700 + i))
		}
		before := d.m.Usage()
		var got []byte
		for _, part := range [][]byte{b[:split], b[split:]} {
			a := d.step(Event{Kind: Stream, Data: part})
			for _, x := range a {
				if x.Kind == WriteLocal {
					if x.Channel != 7 {
						t.Fatalf("split %d wrote channel %d", split, x.Channel)
					}
					got = append(got, x.Data...)
				}
			}
		}
		if d.m.Status() != Active || !bytes.Equal(got, []byte("abc")) || d.m.Usage().Channels != before.Channels {
			t.Fatalf("C16 split %d: status=%v data=%q", split, d.m.Status(), got)
		}
	}
}
