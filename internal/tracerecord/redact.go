package tracerecord

import (
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"net/netip"
)

// Summary is a deny-by-default projection. No strings from events, byte values,
// crypto handles, opaque resources, wire IDs, reserved fields or markers survive.
// Local effect tokens are counters, unrelated to account authentication tokens.
// Numeric/bool omitzero tags preserve v1 omitempty; struct and slice fields
// retain omitempty so their existing representation stays unchanged.
type Summary struct {
	Kind          uint8        `json:"kind"`
	Length        int          `json:"length,omitzero"`
	Type          uint16       `json:"type,omitzero"`
	Count         int          `json:"count,omitzero"`
	Token         uint64       `json:"token,omitzero"`
	Attempt       uint64       `json:"attempt,omitzero"`
	Channel       byte         `json:"channel,omitzero"`
	Generation    uint64       `json:"generation,omitzero"`
	Request       uint64       `json:"request,omitzero"`
	Port          uint16       `json:"port,omitzero"`
	PortAlias     string       `json:"port_alias,omitempty"`
	At            core.Instant `json:"at,omitzero"`
	Peer          string       `json:"peer,omitempty"`
	Transport     uint8        `json:"transport,omitzero"`
	Sequence      rdt.Sequence `json:"wire_sequence,omitempty"`
	Version       byte         `json:"wire_version,omitzero"`
	Transmissions byte         `json:"transmissions,omitzero"`
	Error         string       `json:"error,omitempty"`
	Present       bool         `json:"present,omitzero"`
	Flag          bool         `json:"flag,omitzero"`
	Unsupported   bool         `json:"unsupported,omitzero"`
}

// EdgeEvent describes existing imperative adapters which have no Step core.
// Kind is a fixed local operation code. Edges pass Length without borrowing
// buffers; Data is available for synthetic replay and discarded after summarizing.
type EdgeEvent struct {
	Peer    netip.AddrPort
	Kind    uint8
	Data    []byte
	Length  int
	Outcome string // Fixed DTLS outcome vocabulary, validated below.
	Type    uint16
	Err     error
	Present bool
}

func (w *Writer) peer(a netip.AddrPort) string {
	if !a.IsValid() {
		return ""
	}
	key := a.Addr().String()
	if alias := w.peers[key]; alias != "" {
		return alias
	}
	if len(w.peers) >= 128 {
		return "peerOverflow"
	}
	alias := fmt.Sprintf("peer%d", len(w.peers)+1)
	w.peers[key] = alias
	return alias
}

// portAlias covers every non-service port, irrespective of IP or machine.
func (w *Writer) portAlias(port uint16) string {
	// Zero means unavailable, not an observed port.
	if port == 0 {
		return ""
	}
	if alias := w.ports[port]; alias != "" {
		return alias
	}
	if len(w.ports) >= 128 {
		return "portOverflow"
	}
	if w.ports == nil {
		w.ports = make(map[uint16]string)
	}
	alias := fmt.Sprintf("port%d", len(w.ports)+1)
	w.ports[port] = alias
	return alias
}
func (w *Writer) destinationPort(port uint16) (uint16, string) {
	if w.servicePorts[port] {
		return port, ""
	}
	return 0, w.portAlias(port)
}

func packet(p rdt.Packet) Summary {
	return Summary{Type: uint16(p.Type), Length: len(p.Payload), Sequence: p.Sequence, Version: p.Version, Transmissions: p.Transmissions}
}
func (w *Writer) summarize(v any) Summary {
	switch e := v.(type) {
	case rdt.Event:
		s := Summary{Kind: uint8(e.Kind), Length: len(e.Data), Count: e.Count, Token: e.Token}
		if e.Kind == rdt.Receive {
			s = packet(e.Packet)
			s.Kind = uint8(e.Kind)
		}
		return s
	case rdt.Action:
		s := Summary{Kind: uint8(e.Kind), Length: len(e.Data), Count: e.Count, Token: e.Token, At: e.At, Type: uint16(e.Type)}
		if e.Kind == rdt.Send {
			if p, err := rdt.Decode(e.Data); err == nil {
				h := packet(p)
				s.Type = h.Type
				s.Sequence = h.Sequence
				s.Version = h.Version
				s.Transmissions = h.Transmissions
			}
		}
		return s
	case p2ptunnel.Event:
		port, alias := w.destinationPort(e.Port)
		return Summary{Kind: uint8(e.Kind), Length: len(e.Data), Count: e.Count, Token: e.Token, Channel: e.Channel, Generation: e.Generation, Request: e.Request, Port: port, PortAlias: alias, Flag: e.Failed}
	case p2ptunnel.Action:
		port, alias := w.destinationPort(e.Port)
		s := Summary{Kind: uint8(e.Kind), Length: len(e.Data), Token: e.Token, Channel: e.Channel, Generation: e.Generation, Request: e.Request, Port: port, PortAlias: alias, At: e.At, Flag: e.Temporary, Type: uint16(e.Command)}
		return s
	case iotc.Event:
		s := Summary{Kind: uint8(e.Kind), Length: len(e.Data), Token: e.Token, Attempt: e.Attempt, Peer: w.peer(e.Link.Address), PortAlias: w.portAlias(e.Link.Address.Port()), Transport: uint8(e.Link.Transport), Count: len(e.Addresses)}
		if e.Kind == iotc.Wire {
			if p, err := iotc.ParsePacket(e.Data); err == nil {
				s.Type = p.Opcode
			}
		}
		return s
	case iotc.Action:
		s := Summary{Kind: uint8(e.Kind), Length: len(e.Data), Token: e.Token, Attempt: e.Attempt, Peer: w.peer(e.Link.Address), PortAlias: w.portAlias(e.Link.Address.Port()), Transport: uint8(e.Link.Transport), At: e.At}
		if e.Kind == iotc.Send {
			if p, err := iotc.ParsePacket(e.Data); err == nil {
				s.Type = p.Opcode
			}
		}
		return s
	case cloud.AccountEvent:
		return Summary{Kind: uint8(e.Kind), Token: e.Sequence, Present: e.Token.Value() != "", Error: diagnostic.Class(e.Err)}
	case cloud.AccountAction:
		return Summary{Kind: uint8(e.Kind), Token: e.Sequence, Present: e.Token.Value() != "", Flag: e.Renewal, Error: diagnostic.Class(e.Err)}
	case session.Event:
		return Summary{Kind: uint8(e.Kind), Attempt: e.Attempt, Request: e.Client, Type: uint16(e.Layer), Length: e.Bytes, Count: e.Count, Flag: e.ReleasedAllocation, Error: diagnostic.Class(e.Err)}
	case session.Action:
		return Summary{Kind: uint8(e.Kind), Attempt: e.Attempt, Request: e.Client, At: e.Deadline, Error: diagnostic.Class(e.Err)}
	case EdgeEvent:
		length := len(e.Data)
		if e.Data == nil {
			length = e.Length
		}
		outcome := diagnostic.Class(e.Err)
		if e.Outcome != "" {
			switch e.Outcome {
			case "none", "timeout", "canceled", "operation_failed":
				outcome = e.Outcome
			default:
				outcome = "operation_failed"
			}
		}
		return Summary{Kind: e.Kind, Length: length, Type: e.Type, Present: e.Present, Peer: w.peer(e.Peer), PortAlias: w.portAlias(e.Peer.Port()), Error: outcome}
	default:
		return Summary{Unsupported: true}
	}
}
