package iotc

import (
	"bytes"
	"net/netip"
	"testing"
)

// Independently transcribed decoded frames from the robustness audit C06a/b.
// Their complete outer lengths and distinct TDU extents are intentional.
const layoutMasterResidual = "04021d00b70000000c10180000000000214300004558414d504c45554944303030303030303030300000000000000000000000000000000002009c40c000020a0000000000000000000002000100060001000200030009006d0002002711cb00710100000000000000003059301306072a8648ce3d020106082a8648ce3d030107034200046b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c2964fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f500aa"
const layoutMasterSuffix = "04021d00b70000000c10180000000000214300004558414d504c45554944303030303030303030300000000000000000000000000000000002009c40c000020a0000000000000000000002000100060001000200030009006c0002002711cb00710100000000000000003059301306072a8648ce3d020106082a8648ce3d030107034200046b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c2964fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f500aa"
const layoutKnockPrefix = "04021d001c00000002043300000000004558414d504c45554944303030303030303030304433221188776655"

func layoutPacket(t *testing.T, literal string) Packet {
	t.Helper()
	p, err := ParsePacket(unhex(literal))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLayoutMasterLiterals(t *testing.T) {
	for _, literal := range []string{layoutMasterResidual, layoutMasterSuffix} {
		p := layoutPacket(t, literal)
		c, err := ParseControl(p)
		if err != nil {
			t.Fatalf("complete master extent rejected: %v", err)
		}
		if c.Nonce != 0x4321 || c.UID != [20]byte([]byte("EXAMPLEUID0000000000")) || len(c.Servers) != 1 {
			t.Fatalf("master meaning = %+v", c)
		}
		wantKey := unhex("3059301306072a8648ce3d020106082a8648ce3d030107034200046b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c2964fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f500")
		if c.Servers[0].Address != netip.MustParseAddrPort("203.0.113.1:10001") || !bytes.Equal(c.Servers[0].Public[:], wantKey) {
			t.Fatalf("server meaning = %+v", c.Servers[0])
		}
	}
}

func TestLayoutKnockLiteral(t *testing.T) {
	c, err := ParseControl(layoutPacket(t, layoutKnockPrefix))
	if err != nil {
		t.Fatalf("complete UID/RID prefix rejected: %v", err)
	}
	if c.UID != [20]byte([]byte("EXAMPLEUID0000000000")) || c.RID != [8]byte{0x44, 0x33, 0x22, 0x11, 0x88, 0x77, 0x66, 0x55} || c.Result != 0 {
		t.Fatalf("knock meaning = %+v", c)
	}
}

func TestLayoutMasterBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]byte) []byte
	}{
		{"short-prefix", func(b []byte) []byte { return b[:59] }},
		{"missing-header", func(b []byte) []byte { return b[:61] }},
		{"declared-extent", func(b []byte) []byte { b[72], b[73] = 0xff, 0xff; return b }},
		{"missing-unit", func(b []byte) []byte { b[58] = 3; return b[:len(b)-1] }},
		{"short-required-unit", func(b []byte) []byte { b[62] = 5; return b }},
		{"bad-address", func(b []byte) []byte { clear(b[76:78]); return b }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := layoutPacket(t, layoutMasterResidual)
			p.Body = tc.change(p.Body)
			if _, err := ParseControl(p); err == nil {
				t.Fatal("unsafe master accepted")
			}
		})
	}
	for _, literal := range []string{layoutMasterResidual, layoutMasterSuffix, layoutKnockPrefix} {
		raw := unhex(literal)
		for n := 0; n < len(raw); n++ {
			if _, err := ParsePacket(raw[:n]); err == nil {
				t.Fatalf("truncated frame accepted at %d", n)
			}
		}
		raw = append(raw, 0xaa)
		if _, err := ParsePacket(raw); err == nil {
			t.Fatal("undeclared tail accepted")
		}
	}
}

func TestLayoutKnockConsumedBounds(t *testing.T) {
	for _, op := range []uint16{0x0401, 0x0402, 0x0403, 0x0404, 0x0405, 0x0406} {
		minimum := 28
		if op == 0x0405 {
			minimum = 29
		}
		if op == 0x0406 {
			minimum = 32
		}
		for n := 0; n <= 40; n++ {
			p := layoutPacket(t, layoutKnockPrefix)
			p.Opcode = op
			p.Body = append(p.Body, unhex("01020304aabbccdd9988776655443322")...)[:n]
			c, err := ParseControl(p)
			if (err == nil) != (n >= minimum) {
				t.Fatalf("op=%x n=%d err=%v", op, n, err)
			}
			if err != nil {
				continue
			}
			want := int32(0)
			if op == 0x0405 {
				want = 1
			}
			if op == 0x0406 {
				want = 0x04030201
			}
			if c.Result != want {
				t.Fatalf("op=%x result=%x want=%x", op, c.Result, want)
			}
		}
	}
}

func TestLayoutMasterResidualExtent(t *testing.T) {
	// Keep the audit's complete first entry and vary only the opaque residual.
	// A following declared type-1 TDU must be read at its full declared offset.
	for remainder := 0; remainder < 108; remainder++ {
		p := layoutPacket(t, layoutMasterResidual)
		p.Body = p.Body[:182]
		p.Body = append(p.Body, bytes.Repeat([]byte{0xff}, remainder)...)
		put16(p.Body[72:], uint16(108+remainder))
		p.Body[58] = 3
		p.Body = append(p.Body, unhex("01000600010002000300")...)
		c, err := ParseControl(p)
		if err != nil || len(c.Servers) != 1 || c.Servers[0].Address != netip.MustParseAddrPort("203.0.113.1:10001") {
			t.Fatalf("residual=%d parsed=%+v err=%v", remainder, c, err)
		}
		// Removing a required byte of the next unit is fatal, even with an
		// earlier residual that could be mistaken for spare capacity.
		p.Body = p.Body[:len(p.Body)-1]
		if _, err := ParseControl(p); err == nil {
			t.Fatalf("residual=%d masked a truncated next TDU", remainder)
		}
	}
}
