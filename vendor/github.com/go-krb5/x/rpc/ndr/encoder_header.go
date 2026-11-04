package ndr

// writeCommonHeader writes the NDR common header for Type Serialization v1.
// See header.go for the layout. The encoder always emits little-endian, ASCII,
// IEEE floating point output: version(1), endianness/charenc byte, header
// length(8) and the 0xcccccccc filler.
func (enc *Encoder) writeCommonHeader() error {
	enc.ch.Version = protocolVersion
	enc.ch.HeaderLength = commonHeaderBytes
	enc.ch.CharacterEncoding = ascii
	enc.ch.FloatRepresentation = ieee
	// Version
	if err := enc.buf.WriteByte(protocolVersion); err != nil {
		return Errorf("could not write common header version: %v", err)
	}
	// Endianness (high nibble) and character encoding (low nibble)
	if err := enc.buf.WriteByte(byte(littleEndian)<<4 | enc.ch.CharacterEncoding); err != nil {
		return Errorf("could not write common header endianness: %v", err)
	}
	// Common header length
	b := make([]byte, 2)
	enc.ch.Endianness.PutUint16(b, commonHeaderBytes)
	if err := enc.writeBytes(b); err != nil {
		return Errorf("could not write common header length: %v", err)
	}
	// Filler bytes: MUST be set to 0xcccccccc on marshaling.
	enc.ch.Filler = []byte{0xcc, 0xcc, 0xcc, 0xcc}
	if err := enc.writeBytes(enc.ch.Filler); err != nil {
		return Errorf("could not write common header filler: %v", err)
	}
	return nil
}

// writePrivateHeader writes the NDR private header for Type Serialization v1:
// the object buffer length (which must include padding and exclude the header
// itself) followed by a 4-byte zero filler. The length is backfilled by Encode
// once the body size is known.
func (enc *Encoder) writePrivateHeader(objectBufferLength uint32) error {
	b := make([]byte, 4)
	enc.ch.Endianness.PutUint32(b, objectBufferLength)
	if err := enc.writeBytes(b); err != nil {
		return Errorf("could not write private header object buffer length: %v", err)
	}
	// Filler bytes: MUST be set to 0 during marshaling.
	if err := enc.writeBytes([]byte{0x00, 0x00, 0x00, 0x00}); err != nil {
		return Errorf("could not write private header filler: %v", err)
	}
	return nil
}
