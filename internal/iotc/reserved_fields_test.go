package iotc

import (
	"bytes"
	"testing"
)

// Decoded send headers are canonical even when their input
// came from a permissively decoded receive header.
func TestReservedHeaderSend(t *testing.T) {
	input := unhex("040207ff0f00341207042100443300010c0000004433221188776655616263")
	want := unhex("04021d0b0f00341207042100443300010c0000004433221188776655616263")
	p, err := ParsePacket(input)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Marshal()
	if err != nil || !bytes.Equal(b, want) {
		t.Fatalf("send=%x, %v; want %x", b, err, want)
	}
	for flags := 0; flags < 256; flags++ {
		p.Flags = byte(flags)
		plain, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		wire, err := EncodeLegacy(p, [16]byte{})
		if err != nil {
			t.Fatal(err)
		}
		decoded := Transform(wire, [16]byte{}, flags&1 != 0, true)
		for _, b := range [][]byte{plain, decoded} {
			if b[2] != 29 || b[3]&0xf4 != 0 || b[3]&9 != byte(flags)&9 {
				t.Fatalf("flags=%02x: sent revision=%d flags=%02x", flags, b[2], b[3])
			}
		}
	}
}

func TestReservedHeaderReceive(t *testing.T) {
	// Non-palindromic sequence, opcode, ID and extent expose accidental swapping.
	base := unhex("04021d0b0f00341207042100443300010c0000004433221188776655616263")
	for revision := 0; revision < 256; revision++ {
		for _, flags := range []byte{0x08, 0x0a, 0xfc, 0xfe, 0x09, 0x0b, 0xfd, 0xff} {
			b := bytes.Clone(base)
			b[2], b[3] = byte(revision), flags
			wire := Transform(b, [16]byte{}, flags&1 != 0, false)
			p, err := DecodeLegacy(wire, [16]byte{})
			if err != nil || p.Sequence != 0x1234 || p.Opcode != 0x0407 || p.ID != 0x3344 {
				t.Fatalf("revision=%d flags=%02x: packet=%+v err=%v", revision, flags, p, err)
			}
			rid, data, err := ParseData(p)
			if err != nil || rid != [8]byte{0x44, 0x33, 0x22, 0x11, 0x88, 0x77, 0x66, 0x55} || string(data) != "abc" {
				t.Fatalf("revision=%d flags=%02x: rid=%x data=%x err=%v", revision, flags, rid, data, err)
			}
			var d Decoder
			frames, err := d.Feed(wire[:7], [16]byte{})
			if err != nil || len(frames) != 0 {
				t.Fatal("partial header", err)
			}
			frames, err = d.Feed(wire[7:], [16]byte{})
			if err != nil || len(frames) != 1 || !bytes.Equal(frames[0], wire) || d.Buffered() != 0 {
				t.Fatal("stream header", err)
			}
		}
	}
}
