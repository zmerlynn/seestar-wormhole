package iotc

import "net/netip"

// Server describes a master-advertised sockaddr and full 92-byte ECC slot.
type Server struct {
	Address netip.AddrPort
	Public  [92]byte
}

// Control is a bounded semantic view of the modern control catalog. Opaque
// fields remain available in Packet.Body; none are assigned invented meanings.
type Control struct {
	Unknown               bool // unrecognized opcode; no body fields interpreted
	UID                   [20]byte
	RID                   [8]byte
	Nonce                 uint16
	ServerSID, RelayID    uint32
	Result                int32
	Peer                  netip.AddrPort
	PeerNAT, Capabilities byte
	WAN                   [8]netip.AddrPort
	WANCount              int
	LAN                   [4]netip.AddrPort
	Servers               []Server
}

// ParseControl checks Complete modern control layouts and acceptance lengths.
// Control fields and correlation requirements:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#received-inner-messages
func ParseControl(p Packet) (c Control, err error) {
	b := p.Body
	n := len(b)
	if n > MaxPacket-16 {
		return c, ErrLimit
	}
	uid := func(off int) { copy(c.UID[:], b[off:off+20]) }
	rid := func(off int) { copy(c.RID[:], b[off:off+8]) }
	switch p.Opcode {
	case 0x100c:
		if n < 60 {
			return c, ErrPacket
		}
		c.Nonce = le16(b)
		uid(4)
		count := int(le16(b[58:]))
		off := 60
		if count > (n-off)/4 {
			return c, ErrPacket
		}
		for i := 0; i < count; i++ {
			if off+4 > n {
				return Control{}, ErrPacket
			}
			typ, size := b[off], int(le16(b[off+2:]))
			off += 4
			if size > n-off {
				return Control{}, ErrPacket
			}
			v := b[off : off+size]
			off += size
			switch typ {
			case 1:
				if size < 6 {
					return Control{}, ErrPacket
				}
			case 9:
				// Each server-list unit replaces the preceding list.
				c.Servers = nil
				// Entry count is the declared extent divided by 108; a
				// partial trailing slot has no fields we can consume.
				for j := 0; j+108 <= size; j += 108 {
					a, e := ParseAddress(v[j : j+16])
					if e != nil {
						return Control{}, e
					}
					var s Server
					s.Address = a
					copy(s.Public[:], v[j+16:j+108])
					found := false
					for _, old := range c.Servers {
						if old.Address == a {
							found = true
							break
						}
					}
					if !found {
						if len(c.Servers) == 12 {
							return Control{}, ErrLimit
						}
						c.Servers = append(c.Servers, s)
					}
				}
			}
		}
		// The declared TDU count delimits the consumed fields. Remaining
		// bytes are opaque within the already bounded, complete frame.
	case 0x0218:
		if n != 56 {
			return c, ErrPacket
		}
		c.Nonce = le16(b)
		c.ServerSID = le32(b[4:])
		uid(8)
		c.PeerNAT = byte(le32(b[44:]))
		c.Capabilities = b[53]
	case 0x030d:
		if n != 296 {
			return c, ErrPacket
		}
		c.Nonce = le16(b)
		uid(4)
		rid(104)
		c.PeerNAT = byte(le32(b[116:]))
		add := func(v []byte) error {
			a, e := ParseAddress(v)
			if e != nil {
				return e
			}
			for _, old := range c.WAN[:c.WANCount] {
				if a == old {
					return nil
				}
			}
			if c.WANCount < 8 {
				c.WAN[c.WANCount] = a
				c.WANCount++
			}
			return nil
		}
		if e := add(b[24:40]); e != nil {
			return Control{}, e
		}
		for j := 0; j < 4; j++ {
			v := b[40+j*16 : 56+j*16]
			if le32(v[4:]) != 0 {
				a, e := ParseAddress(v)
				if e != nil {
					return Control{}, e
				}
				c.LAN[j] = a
			}
		}
		for j := 0; j < 8; j++ {
			v := b[120+j*16 : 136+j*16]
			if le32(v[4:]) == 0 {
				break
			}
			if e := add(v); e != nil {
				return Control{}, e
			}
		}
	case 0x020d:
		if n != 32 {
			return c, ErrPacket
		}
		uid(0)
		c.Result = int32(le32(b[20:]))
		rid(24)
	case 0x030e:
		if n != 88 {
			return c, ErrPacket
		}
		uid(0)
		c.RelayID = le32(b[20:])
		c.Peer, err = ParseAddress(b[40:56])
		if err != nil {
			return Control{}, err
		}
		rid(56)
		c.PeerNAT = byte(le32(b[84:]))
	case 0x0217:
		if n != 36 {
			return c, ErrPacket
		}
		c.Nonce = le16(b)
		uid(4)
	case 0x0240:
		if n != 272 {
			return c, ErrPacket
		}
		uid(0)
		rid(84)
	case 0x010d:
		if n != 36 {
			return c, ErrPacket
		}
		c.Nonce = le16(b)
		uid(4)
		rid(24)
	case 0x030f:
		if n != 28 {
			return c, ErrPacket
		}
		uid(0)
		rid(20)
		c.RelayID = uint32(p.ID)
	case 0x0129, 0x012b:
		if n != 24 {
			return c, ErrPacket
		}
		c.Nonce = le16(b)
		uid(4)
	case 0x0401, 0x0402, 0x0403, 0x0404, 0x0405, 0x0406:
		minimum := 28 // Complete extended UID20 + RID8; no short-RID mode.
		if p.Opcode == 0x0405 {
			minimum = 29 // Only this response consumes the result byte.
		} else if p.Opcode == 0x0406 {
			minimum = 32 // Final response consumes the whole LE32 result.
		}
		if n < minimum {
			return c, ErrPacket
		}
		uid(0)
		rid(20)
		if p.Opcode == 0x0405 {
			c.Result = int32(b[28])
		} else if p.Opcode == 0x0406 {
			c.Result = int32(le32(b[28:]))
		}
	default:
		c.Unknown = true
	}
	return c, nil
}

// Alert is a complete authenticated type-14 plaintext. See alert bounds correction.
type Alert struct {
	ServerSID               uint32
	Nonce                   uint16
	Code, Trigger, Protocol byte
}

// ParseAlert rejects truncated alerts before the machine can commit any SID.
// Require the full alert and correlate SID/nonce before state changes:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/relay.md#server-alerts
func ParseAlert(b []byte) (Alert, error) {
	if len(b) < 9 || len(b) > MaxPacket-28 {
		return Alert{}, ErrPacket
	}
	return Alert{le32(b), le16(b[4:]), b[6], b[7], b[8]}, nil
}

func controlPacket(op, route uint16, n int) Packet {
	return Packet{Revision: 29, Opcode: op, Route: route, Body: make([]byte, n)}
}

// Master plaintext offsets and fixed-width realm/UID fields:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/master-record.md#master-query-record-type-1
func masterQuery(uid [20]byte, realm string, m Material) Packet {
	p := controlPacket(0x100b, 0x18, 72)
	b := p.Body
	put16(b, m.MasterNonce)
	copy(b[4:], m.MasterKey[:])
	copy(b[20:], m.MasterIV[:])
	copy(b[32:], uid[:])
	copy(b[52:68], realm)
	b[68] = 6
	return p
}

// The precheck carries the optional IOTC auth field, not tunnel credentials:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/master-record.md#initial-handshake-record-type-2
func precheck(uid [20]byte, m Material) Packet {
	p := controlPacket(0x0217, 0x24, 36)
	put16(p.Body, m.P2PNonce)
	copy(p.Body[4:], uid[:])
	return p
}

// Selected relay-info flag 1 and eligible unselected flag 0:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/relay.md#record-relay-messages-client-sdk
func rendezvous(op uint16, uid [20]byte, m Material, nat byte, relay uint16, selected bool) Packet {
	var p Packet
	switch op {
	case 0x0240:
		p = controlPacket(op, 0x34, 272)
		copy(p.Body[84:], m.RID[:])
		put32(p.Body[92:], uint32(nat))
	case 0x020c:
		p = controlPacket(op, 0x24, 48)
		copy(p.Body[20:], m.RID[:])
		put32(p.Body[28:], 60)
		put32(p.Body[32:], 5)
	case 0x0241:
		// The control record uses the complete modern layout.
		p = controlPacket(op, 0x24, 44)
		copy(p.Body[20:], m.RID[:])
		if selected {
			put32(p.Body[28:], 1)
		}
		put32(p.Body[32:], 0x04030800)
		put32(p.Body[36:], 29)
		put32(p.Body[40:], uint32(nat))
	case 0x030f:
		p = controlPacket(op, 0x24, 28)
		copy(p.Body[20:], m.RID[:])
		p.ID = relay
	}
	copy(p.Body, uid[:])
	return p
}
func knock(op uint16, uid [20]byte, rid [8]byte) Packet {
	p := controlPacket(op, 0x33, 36)
	copy(p.Body, uid[:])
	copy(p.Body[20:], rid[:])
	put32(p.Body[32:], 0x04030800)
	return p
}
