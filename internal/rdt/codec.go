package rdt

import (
	"encoding/binary"
	"errors"
)

const (
	HeaderSize = 20
	MaxData    = 1280
	// MaxPacket is local receive policy, matching the secure application cap.
	MaxPacket        = HeaderSize + MaxData
	SACKSize         = 72
	Magic     uint32 = 0xf1c2975a
	Version   byte   = 5
)

type Type byte

const (
	Heartbeat    Type = 0x01
	Data         Type = 0x02
	FIN          Type = 0x03
	Quit         Type = 0x04
	Urgent       Type = 0x10
	Abort        Type = 0x20
	HeartbeatACK Type = 0x41
	DataACK      Type = 0x42
	FINACK       Type = 0x43
	SACK2        Type = 0x45
	ACK2         Type = 0x46
	UrgentACK    Type = 0x50
	AbortACK     Type = 0x60
	Exit         Type = 0x70
	NeedDTLS     Type = 0x71
)

var ErrPacket = errors.New("invalid RDT packet")

// Sequence compares high then low; the low word rolls after 0xf0000000.
// Use packet-pair order and the nonstandard low-word successor:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#sequence-numbers
type Sequence struct{ Low, High uint32 }

func (s Sequence) Less(t Sequence) bool { return s.High < t.High || s.High == t.High && s.Low < t.Low }
func (s Sequence) Next() Sequence {
	if s.Low < 0xf0000000 {
		s.Low++
	} else {
		s.Low = 0
		s.High++
	}
	return s
}
func readSequence(b []byte) Sequence {
	return Sequence{binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint32(b[4:])}
}
func putSequence(b []byte, s Sequence) {
	binary.LittleEndian.PutUint32(b, s.Low)
	binary.LittleEndian.PutUint32(b[4:], s.High)
}

// Packet owns its payload after Decode. Reserved is opaque and preserved, as ACK
// constructors copy it. Version is preserved; peer policy belongs to the machine.
type Packet struct {
	Type              Type
	Version           byte
	Sequence          Sequence
	Transmissions, ID byte
	Reserved          uint16
	Payload           []byte
}

func valid(t Type, p []byte) bool {
	switch t {
	case Data, Urgent:
		return len(p) > 0 && len(p) <= MaxData
	case SACK2:
		s, err := DecodeSACK(p)
		return err == nil && len(p) == SACKSize && s.Count <= 4
	case Heartbeat, HeartbeatACK, DataACK, FIN, FINACK, Quit, Abort, AbortACK, ACK2, UrgentACK, Exit, NeedDTLS:
		return len(p) == 0
	default:
		return false
	}
}

// receiveValid keeps known layouts strict but skips bounded unknown types.
// Unknown message types have no operation.
func receiveValid(t Type, p []byte) bool {
	if len(p) > MaxData {
		return false
	}
	if !known(t) {
		return true
	}
	if t == SACK2 {
		_, err := DecodeSACK(p)
		return err == nil
	}
	return valid(t, p)
}

func known(t Type) bool {
	switch t {
	case Heartbeat, Data, FIN, Quit, Urgent, Abort, HeartbeatACK, DataACK, FINACK, SACK2, ACK2, UrgentACK, AbortACK, Exit, NeedDTLS:
		return true
	}
	return false
}

// Wire layout and bounded validation:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/framing.md#decoding-rules-for-new-implementations
func Decode(b []byte) (Packet, error) {
	if len(b) < HeaderSize || len(b) > MaxPacket || binary.LittleEndian.Uint32(b) != Magic || int(binary.LittleEndian.Uint16(b[6:])) != len(b)-HeaderSize || !receiveValid(Type(b[4]), b[HeaderSize:]) {
		return Packet{}, ErrPacket
	}
	return Packet{Type(b[4]), b[5], readSequence(b[8:]), b[16], b[17], binary.LittleEndian.Uint16(b[18:]), append([]byte(nil), b[20:]...)}, nil
}

// Wire layout and bounded validation:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/framing.md#header
func (p Packet) Encode() ([]byte, error) {
	if !valid(p.Type, p.Payload) {
		return nil, ErrPacket
	}
	b := make([]byte, HeaderSize+len(p.Payload))
	binary.LittleEndian.PutUint32(b, Magic)
	b[4] = byte(p.Type)
	b[5] = p.Version
	binary.LittleEndian.PutUint16(b[6:], uint16(len(p.Payload)))
	putSequence(b[8:], p.Sequence)
	b[16] = p.Transmissions
	b[17] = p.ID
	binary.LittleEndian.PutUint16(b[18:], p.Reserved)
	copy(b[20:], p.Payload)
	return b, nil
}

type Range struct{ Start, End Sequence }

// SACK retains all complete entries within the bounded payload.
// Encode emits only the strict 72-byte form; receive capacity may differ.
// Only Entries[:Count] acknowledges packets; the header watermark does not.
type SACK struct {
	Unused  uint32
	Count   uint32
	Entries []Range
}

// Inclusive ranges are bounded by the payload and a four-entry ceiling:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/framing.md#data_sack2-payload
// R23 requires checking supplied capacity before reading ranges:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/framing.md#decoding-rules-for-new-implementations
func DecodeSACK(b []byte) (SACK, error) {
	var s SACK
	if len(b) < 8 || len(b) > MaxData {
		return s, ErrPacket
	}
	s.Unused = binary.LittleEndian.Uint32(b)
	s.Count = binary.LittleEndian.Uint32(b[4:])
	// DATA_SACK2 uses a signed-positive count,
	// bounded by supplied capacity before allocating or indexing entries.
	capacity := (len(b) - 8) / 16
	if int32(s.Count) <= 0 || s.Count > 4 || uint64(s.Count) > uint64(capacity) {
		return SACK{}, ErrPacket
	}
	s.Entries = make([]Range, capacity)
	for i := range s.Entries {
		s.Entries[i] = Range{readSequence(b[8+i*16:]), readSequence(b[16+i*16:])}
		if uint32(i) < s.Count && s.Entries[i].End.Less(s.Entries[i].Start) {
			return SACK{}, ErrPacket
		}
	}
	return s, nil
}
func (s SACK) Encode() ([]byte, error) {
	if s.Count < 1 || s.Count > 4 || len(s.Entries) > 4 || uint64(s.Count) > uint64(len(s.Entries)) {
		return nil, ErrPacket
	}
	b := make([]byte, SACKSize)
	binary.LittleEndian.PutUint32(b, s.Unused)
	binary.LittleEndian.PutUint32(b[4:], s.Count)
	for i, r := range s.Entries {
		if uint32(i) < s.Count && r.End.Less(r.Start) {
			return nil, ErrPacket
		}
		putSequence(b[8+i*16:], r.Start)
		putSequence(b[16+i*16:], r.End)
	}
	return b, nil
}
