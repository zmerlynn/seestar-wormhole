package ndr

import (
	"math"
)

// writeBool writes a byte representing a boolean.
// NDR represents a Boolean as one octet: FALSE as a zero octet, TRUE as a
// non-zero octet.
func (enc *Encoder) writeBool(b bool) error {
	if b {
		return enc.writeUint8(1)
	}
	return enc.writeUint8(0)
}

// writeUint8 writes a byte representing an 8bit unsigned integer.
func (enc *Encoder) writeUint8(i uint8) error {
	return enc.buf.WriteByte(i)
}

// writeUint16 writes bytes representing a 16bit unsigned integer.
func (enc *Encoder) writeUint16(i uint16) error {
	enc.ensureAlignment(SizeUint16)
	b := make([]byte, SizeUint16)
	enc.ch.Endianness.PutUint16(b, i)
	return enc.writeBytes(b)
}

// writeUint32 writes bytes representing a 32bit unsigned integer.
func (enc *Encoder) writeUint32(i uint32) error {
	enc.ensureAlignment(SizeUint32)
	b := make([]byte, SizeUint32)
	enc.ch.Endianness.PutUint32(b, i)
	return enc.writeBytes(b)
}

// writeUint64 writes bytes representing a 64bit unsigned integer.
func (enc *Encoder) writeUint64(i uint64) error {
	enc.ensureAlignment(SizeUint64)
	b := make([]byte, SizeUint64)
	enc.ch.Endianness.PutUint64(b, i)
	return enc.writeBytes(b)
}

func (enc *Encoder) writeInt8(i int8) error {
	enc.ensureAlignment(SizeUint8)
	return enc.writeUint8(uint8(i))
}

func (enc *Encoder) writeInt16(i int16) error {
	return enc.writeUint16(uint16(i))
}

func (enc *Encoder) writeInt32(i int32) error {
	return enc.writeUint32(uint32(i))
}

func (enc *Encoder) writeInt64(i int64) error {
	return enc.writeUint64(uint64(i))
}

// https://en.wikipedia.org/wiki/IEEE_754-1985
func (enc *Encoder) writeFloat32(f float32) error {
	return enc.writeUint32(math.Float32bits(f))
}

func (enc *Encoder) writeFloat64(f float64) error {
	return enc.writeUint64(math.Float64bits(f))
}

// ensureAlignment pads the output stream with zero bytes so that the next
// primitive of size n octets is aligned at an octet stream index that is a
// multiple of n. This is the symmetric counterpart of the decoder skipping
// alignment gaps. The index is relative to the start of the whole stream.
func (enc *Encoder) ensureAlignment(n int) {
	p := enc.buf.Len()
	if s := p % n; s != 0 {
		enc.buf.Write(make([]byte, n-s))
	}
}
