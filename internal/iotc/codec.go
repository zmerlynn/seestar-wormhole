package iotc

import (
	"encoding/binary"
	"errors"
	"math/bits"
	"net/netip"
)

// MaxPacket is the maximum received packet size.
const MaxPacket = 1416

// MaxData is the maximum DTLS datagram size.
const MaxData = 1388

var (
	// ErrPacket denotes malformed or unsupported framing; it never contains input bytes.
	ErrPacket = errors.New("iotc: invalid packet")
	// ErrLimit denotes an exhausted local resource bound.
	ErrLimit = errors.New("iotc: resource limit")
)

// Packet is an unscrambled legacy header and borrowed body. See Legacy wire framing.
type Packet struct {
	Revision, Flags             byte
	Sequence, Opcode, Route, ID uint16
	Channel, Type               byte
	Body                        []byte
}

// ParsePacket requires one whole decoded frame, including exact length agreement.
// Wire layout and receive boundary:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#legacy-header
func ParsePacket(b []byte) (Packet, error) {
	if len(b) < 16 || len(b) > MaxPacket || binary.LittleEndian.Uint16(b) != 0x0204 || int(binary.LittleEndian.Uint16(b[4:])) != len(b)-16 {
		return Packet{}, ErrPacket
	}
	return Packet{b[2], b[3], le16(b[6:]), le16(b[8:]), le16(b[10:]), le16(b[12:]), b[14], b[15], b[16:]}, nil
}

// Marshal allocates an owned decoded packet with the canonical sender revision
// and flags.
// Canonical sender revision and flag bits:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#revision-and-flag-bits-on-receive
func (p Packet) Marshal() ([]byte, error) {
	if len(p.Body) > MaxPacket-16 {
		return nil, ErrLimit
	}
	b := make([]byte, 16+len(p.Body))
	put16(b, 0x0204)
	b[2] = 29
	b[3] = p.Flags & 0x0b
	put16(b[4:], uint16(len(p.Body)))
	put16(b[6:], p.Sequence)
	put16(b[8:], p.Opcode)
	put16(b[10:], p.Route)
	put16(b[12:], p.ID)
	b[14] = p.Channel
	b[15] = p.Type
	copy(b[16:], p.Body)
	return b, nil
}
func le16(b []byte) uint16     { return binary.LittleEndian.Uint16(b) }
func le32(b []byte) uint32     { return binary.LittleEndian.Uint32(b) }
func put16(b []byte, v uint16) { binary.LittleEndian.PutUint16(b, v) }
func put32(b []byte, v uint32) { binary.LittleEndian.PutUint32(b, v) }

// ScrambleKey derives the legacy obfuscation key from a verified license mask.
// Only the first 16 bytes of the phrase derived from the shipped binary are used.
// Wire layout and receive boundary:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#key
func ScrambleKey(mask [16]byte) (key [16]byte) {
	copy(key[:], "Charlie is the d")
	for i := range key {
		key[i] ^= mask[i]
	}
	return
}

// Transform implements Scrambling, exactly. This is obfuscation, not crypto.
// Partial mode transforms at most 64 bytes including the legacy header.
// Wire layout and receive boundary:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#scrambling
func Transform(src []byte, key [16]byte, partial, inverse bool) []byte {
	out := append([]byte(nil), src...)
	n := len(out)
	if partial {
		n = min(n, 64)
	}
	perm := [16]int{11, 9, 8, 15, 13, 10, 12, 14, 2, 1, 5, 0, 6, 4, 7, 3}
	for off := 0; off+16 <= n; off += 16 {
		b := out[off : off+16]
		first, last := [4]int{-1, -5, -9, -13}, [4]int{-3, -7, -11, -15}
		if inverse {
			first, last = [4]int{3, 7, 11, 15}, [4]int{1, 5, 9, 13}
		}
		for j := 0; j < 4; j++ {
			put32(b[j*4:], bits.RotateLeft32(le32(b[j*4:]), first[j]))
		}
		if !inverse {
			for j := range b {
				b[j] ^= key[j]
			}
		}
		var tmp [16]byte
		copy(tmp[:], b)
		for j := range b {
			b[j] = tmp[perm[j]]
		}
		if inverse {
			for j := range b {
				b[j] ^= key[j]
			}
		}
		for j := 0; j < 4; j++ {
			put32(b[j*4:], bits.RotateLeft32(le32(b[j*4:]), last[j]))
		}
	}
	tail := out[n/16*16 : n]
	var p []int
	switch len(tail) {
	case 2:
		p = []int{1, 0}
	case 4:
		p = []int{2, 3, 0, 1}
	case 8:
		p = []int{7, 4, 3, 2, 1, 6, 5, 0}
	}
	if !inverse {
		for j := range tail {
			tail[j] ^= key[j]
		}
	}
	var tmp [16]byte
	copy(tmp[:], tail)
	for j, k := range p {
		tail[j] = tmp[k]
	}
	if inverse {
		for j := range tail {
			tail[j] ^= key[j]
		}
	}
	return out
}

// DecodeLegacy reverses the header first to determine full versus partial mode.
func DecodeLegacy(b []byte, key [16]byte) (Packet, error) {
	if len(b) < 16 || len(b) > MaxPacket {
		return Packet{}, ErrPacket
	}
	h := Transform(b[:16], key, false, true)
	if le16(h) != 0x0204 || int(le16(h[4:])) != len(b)-16 {
		return Packet{}, ErrPacket
	}
	return ParsePacket(Transform(b, key, h[3]&1 != 0, true))
}

// EncodeLegacy sets the sender endian flag and scrambles a complete packet.
func EncodeLegacy(p Packet, key [16]byte) ([]byte, error) {
	p.Flags |= 2
	b, e := p.Marshal()
	if e != nil {
		return nil, e
	}
	return Transform(b, key, p.Flags&1 != 0, false), nil
}

// ParseData extracts extended RID and borrowed application payload. Short RID is
// deliberately excluded from this revision-29 client profile.
// Wire layout and receive boundary:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#data-alive-and-close-packets
func ParseData(p Packet) (rid [8]byte, data []byte, err error) {
	switch p.Opcode {
	case 0x0407, 0x0408, 0x0501, 0x0502, 0x0503, 0x0504:
	default:
		return rid, nil, ErrPacket
	}
	if p.Flags&8 == 0 || p.Channel > 31 || len(p.Body) < 12 || le32(p.Body) != 12 || len(p.Body)-12 > MaxData {
		return rid, nil, ErrPacket
	}
	copy(rid[:], p.Body[4:12])
	return rid, p.Body[12:], nil
}

// Record is a borrowed modern envelope; crypto authentication is an edge effect.
type Record struct {
	Type byte
	SID  uint32
	Body []byte
}

// Unknown reports an unrecognized envelope type. Such records are logged and
// dropped after framing checks, without crypto or body interpretation.
func (r Record) Unknown() bool {
	switch r.Type {
	case 1, 2, 3, 4, 5, 14:
		return false
	default:
		return true
	}
}

// ParseRecord validates the modern envelope, not its type or encrypted contents.
// Wire layout and receive boundary:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#envelope
func ParseRecord(b []byte) (Record, error) {
	if len(b) < 12 || len(b) > MaxPacket || le32(b) != 0xcc51 || b[4] != 2 || int(le16(b[6:])) != len(b)-12 {
		return Record{}, ErrPacket
	}
	return Record{b[5], le32(b[8:]), b[12:]}, nil
}

// RecordHeader constructs the exact AAD envelope for a known final body length.
// Wire layout and receive boundary:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#envelope
func RecordHeader(typ byte, sid uint32, n int) ([]byte, error) {
	if n < 0 || n > MaxPacket-12 {
		return nil, ErrLimit
	}
	b := make([]byte, 12)
	put32(b, 0xcc51)
	b[4] = 2
	b[5] = typ
	put16(b[6:], uint16(n))
	put32(b[8:], sid)
	return b, nil
}

// Decoder delimits mixed legacy and modern TCP records with fixed storage.
// Unlike the native resynchronizer, corrupt framing fails closed. The caller
// closes that stream on error. Feed input is capped at 8192 bytes per owner turn.
// Closing corrupt TCP framing is local policy instead of native resynchronization:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#tcp-reception
type Decoder struct {
	buf  [MaxPacket]byte
	n    int
	want int
}

// Buffered reports retained stream bytes.
func (d *Decoder) Buffered() int { return d.n }

// Feed owns its returned complete wire records and borrows input only during call.
// Wire layout and receive boundary:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#udptcp-race
func (d *Decoder) Feed(b []byte, key [16]byte) ([][]byte, error) {
	if len(b) > 8192 {
		return nil, ErrLimit
	}
	var frames [][]byte
	for len(b) > 0 {
		target := d.want
		if target == 0 {
			target = 12
		}
		if d.n >= 12 && d.want == 0 && le32(d.buf[:]) != 0xcc51 {
			target = 16
		}
		k := min(target-d.n, len(b))
		copy(d.buf[d.n:], b[:k])
		d.n += k
		b = b[k:]
		if d.want == 0 && d.n >= 12 {
			if le32(d.buf[:]) == 0xcc51 {
				if d.buf[4] != 2 {
					return nil, ErrPacket
				}
				d.want = 12 + int(le16(d.buf[6:]))
			} else if d.n == 16 {
				h := Transform(d.buf[:16], key, false, true)
				if le16(h) != 0x0204 {
					return nil, ErrPacket
				}
				d.want = 16 + int(le16(h[4:]))
			}
			if d.want > MaxPacket {
				return nil, ErrLimit
			}
		}
		if d.want > 0 && d.n == d.want {
			frames = append(frames, append([]byte(nil), d.buf[:d.n]...))
			d.n = 0
			d.want = 0
		}
	}
	return frames, nil
}

// ParseAddress reads the IPv4 sockaddr16 described by the control tables. Family
// and trailing bytes from received records are opaque; only port/IP are consumed.
// Wire layout and receive boundary:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#common-field-types
func ParseAddress(b []byte) (netip.AddrPort, error) {
	if len(b) != 16 {
		return netip.AddrPort{}, ErrPacket
	}
	ip := netip.AddrFrom4([4]byte(b[4:8]))
	port := binary.BigEndian.Uint16(b[2:])
	if ip.IsUnspecified() || port == 0 {
		return netip.AddrPort{}, ErrPacket
	}
	return netip.AddrPortFrom(ip, port), nil
}
func addressBytes(a netip.AddrPort) []byte {
	b := make([]byte, 16)
	if a.IsValid() && a.Addr().Is4() {
		put16(b, 2)
		binary.BigEndian.PutUint16(b[2:], a.Port())
		v := a.Addr().As4()
		copy(b[4:], v[:])
	}
	return b
}
