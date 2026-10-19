package p2ptunnel

import (
	"bytes"
	"encoding/binary"
	"errors"
)

const (
	HeaderSize           = 4
	MaxData              = 8192
	MaxAuth              = 1000
	AgentVersion  uint32 = 0x04030800
	ServerVersion uint32 = 0x04030603
)

type Command byte

const (
	SetupCommand    Command = 1
	DataCommand     Command = 2
	EndCommand      Command = 3
	CloseCommand    Command = 4
	PauseCommand    Command = 5
	ResumeCommand   Command = 6
	WakeCommand     Command = 7
	LoginCommand    Command = 7
	PassCommand     Command = 8
	RejectCommand   Command = 9
	PortRequest     Command = 12
	PortResponse    Command = 13
	ServiceRequest  Command = 14
	ServiceResponse Command = 15
)

type Phase byte

const (
	Established Phase = iota
	LoginRequest
	LoginResponse
)

var ErrFrame = errors.New("invalid tunnel frame")

// Frame owns its decoded body. Opcode 7 is interpreted only with an explicit Phase.
type Frame struct {
	Command Command
	Channel byte
	Payload []byte
}

// Send headers use canonical lengths for the current session phase:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#message-catalogue
func headerValid(cmd Command, ch byte, n int, phase Phase) bool {
	switch phase {
	case LoginRequest:
		return cmd == LoginCommand && ch == 0 && (n == 0 || n >= 8 && n <= 8+MaxAuth)
	case LoginResponse:
		return (cmd == PassCommand || cmd == RejectCommand) && ch == 0 && n == 8
	case Established:
		switch cmd {
		case SetupCommand:
			return n == 16 || n == 52
		case DataCommand:
			return n > 0 && n <= MaxData
		case EndCommand, PauseCommand, ResumeCommand, WakeCommand:
			return n == 0
		case CloseCommand:
			return n <= 1
		case PortRequest, PortResponse:
			return ch == 0 && n == 6
		case ServiceRequest, ServiceResponse:
			return ch == 0 && n == 36
		}
	}
	return false
}

// receiveHeaderValid is distinct from strict send validation. Unknown commands
// have no established-session operation, but their declared body must still fit
// the receive cap.
// Receive framing validates the declared extent before consuming a frame:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#frame-envelope
func receiveHeaderValid(cmd Command, ch byte, n int, phase Phase) bool {
	if n < 0 || n > MaxData {
		return false
	}
	// Session-level receive identity is phase or the consumed check ID, not
	// the unused header channel. Send policy still requires channel zero.
	if phase == LoginRequest || phase == LoginResponse || phase == Established && (cmd == PortRequest || cmd == PortResponse || cmd == ServiceRequest || cmd == ServiceResponse) {
		ch = 0
	}
	if phase == LoginResponse {
		// Wait for exactly header plus bounded L. Accept every
		// complete PASS length; other opcodes never grant login.
		return cmd == PassCommand || cmd == RejectCommand
	}
	if phase == Established && !knownCommand(cmd) {
		return true
	}
	if phase == Established && cmd == SetupCommand {
		// Setup accepts the 16-byte legacy body or an extended body of at least
		// 52 bytes.
		return n == 16 || n >= 52
	}
	if phase == Established && cmd == CloseCommand {
		// A nonempty close body is diagnostic only; release does not drain it.
		return true
	}
	return headerValid(cmd, ch, n, phase)
}

func knownCommand(cmd Command) bool {
	switch cmd {
	case SetupCommand, DataCommand, EndCommand, CloseCommand, PauseCommand, ResumeCommand, WakeCommand, PortRequest, PortResponse, ServiceRequest, ServiceResponse:
		return true
	}
	return false
}

// validateBody checks only consumed fields after either header policy passed.
func (f Frame) validateBody(phase Phase) error {
	if phase == LoginRequest {
		_, err := ParseLogin(f.Payload)
		return err
	}
	if phase == Established {
		switch f.Command {
		case SetupCommand:
			_, err := ParseSetup(f.Payload)
			return err
		case ServiceRequest, ServiceResponse:
			_, err := ParseService(f.Payload)
			return err
		}
	}
	return nil
}
func (f Frame) receiveValidate(phase Phase) error {
	if !receiveHeaderValid(f.Command, f.Channel, len(f.Payload), phase) {
		return ErrFrame
	}
	return f.validateBody(phase)
}

// Validate applies strict send policy; Decode uses the separate receive policy.
func (f Frame) Validate(phase Phase) error {
	if !headerValid(f.Command, f.Channel, len(f.Payload), phase) {
		return ErrFrame
	}
	if phase == Established && f.Command == SetupCommand {
		// Raw send frames must have the same terminated,
		// zero-filled fields as the typed builder. Receive remains permissive.
		s, err := ParseSetup(f.Payload)
		if err != nil {
			return err
		}
		canonical, err := s.Encode()
		if err != nil || !bytes.Equal(canonical, f.Payload) {
			return ErrFrame
		}
	}
	if phase == Established {
		// Validity is a boolean on send, and request/spare
		// bytes are zero. These constraints do not apply on receive.
		switch f.Command {
		case PortRequest, PortResponse:
			if f.Payload[5] != 0 || f.Payload[4] > 1 || f.Command == PortRequest && f.Payload[4] != 0 {
				return ErrFrame
			}
		case ServiceRequest, ServiceResponse:
			s, err := ParseService(f.Payload)
			if err != nil {
				return err
			}
			canonical, err := s.Encode()
			if err != nil || !bytes.Equal(canonical, f.Payload) || f.Command == ServiceRequest && s.Valid != 0 {
				return ErrFrame
			}
		}
	}
	return f.validateBody(phase)
}
func Decode(b []byte, phase Phase) (Frame, error) {
	if len(b) < 4 || int(binary.LittleEndian.Uint16(b[2:])) != len(b)-4 {
		return Frame{}, ErrFrame
	}
	f := Frame{Command(b[0]), b[1], b[4:]}
	if err := f.receiveValidate(phase); err != nil {
		return Frame{}, err
	}
	f.Payload = bytes.Clone(f.Payload)
	return f, nil
}
func (f Frame) Encode(phase Phase) ([]byte, error) {
	if err := f.Validate(phase); err != nil {
		return nil, err
	}
	b := make([]byte, 4+len(f.Payload))
	b[0] = byte(f.Command)
	b[1] = f.Channel
	binary.LittleEndian.PutUint16(b[2:], uint16(len(f.Payload)))
	copy(b[4:], f.Payload)
	return b, nil
}

// Decoder retains at most one frame (8196 bytes), never an entire stream read.
// Feed consumes through one frame and reports bytes used; call again with surplus
// and the new phase after login. A framing error poisons the decoder until Reset.
type Decoder struct {
	buf        [HeaderSize + MaxData]byte
	used, want int
	failed     bool
}

func (d *Decoder) Reset()        { *d = Decoder{} }
func (d *Decoder) Buffered() int { return d.used }

// Feed reassembles exactly one header and its declared payload across stream reads:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#frame-envelope
func (d *Decoder) Feed(input []byte, phase Phase) (int, *Frame, error) {
	if d.failed {
		return 0, nil, ErrFrame
	}
	consumed := 0
	for {
		target := d.want
		if target == 0 {
			target = 4
		}
		n := min(target-d.used, len(input))
		copy(d.buf[d.used:], input[:n])
		d.used += n
		consumed += n
		input = input[n:]
		if d.used < target {
			return consumed, nil, nil
		}
		if d.want == 0 {
			length := int(binary.LittleEndian.Uint16(d.buf[2:4]))
			if !receiveHeaderValid(Command(d.buf[0]), d.buf[1], length, phase) {
				d.failed = true
				return consumed, nil, ErrFrame
			}
			d.want = 4 + length
			if d.used < d.want {
				if len(input) == 0 {
					return consumed, nil, nil
				}
				continue
			}
		}
		f, err := Decode(d.buf[:d.want], phase)
		if err != nil {
			d.failed = true
			return consumed, nil, err
		}
		d.used = 0
		d.want = 0
		return consumed, &f, nil
	}
}

type Login struct {
	Version uint32
	Auth    []byte
	Empty   bool
}

// ParseLogin checks the declared authentication-data length before retaining it:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#login_auth-07-agent---server
func ParseLogin(b []byte) (Login, error) {
	if len(b) == 0 {
		return Login{Empty: true}, nil
	}
	if len(b) < 8 || len(b) > 8+MaxAuth || uint64(binary.LittleEndian.Uint32(b[4:])) != uint64(len(b)-8) {
		return Login{}, ErrFrame
	}
	return Login{Version: binary.LittleEndian.Uint32(b), Auth: bytes.Clone(b[8:])}, nil
}
func (v Login) Encode() ([]byte, error) {
	if v.Empty {
		if len(v.Auth) != 0 || v.Version != 0 {
			return nil, ErrFrame
		}
		return nil, nil
	}
	if len(v.Auth) > MaxAuth {
		return nil, ErrFrame
	}
	b := make([]byte, 8+len(v.Auth))
	binary.LittleEndian.PutUint32(b, v.Version)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(v.Auth)))
	copy(b[8:], v.Auth)
	return b, nil
}

type LoginResult struct {
	Result                                      int32
	PortVerify, Direct, ServiceVerify, Reserved byte
}

// ParseLoginResult accepts short PASS payloads with capabilities disabled (R15):
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#login_pass--login_reject-08--09-server---agent
func ParseLoginResult(b []byte) (LoginResult, error) {
	if len(b) > MaxData {
		return LoginResult{}, ErrFrame
	}
	// L below eight disables all capabilities. The signed result does
	// not decide PASS acceptance; consume it only when actually present.
	var r LoginResult
	if len(b) >= 4 {
		r.Result = int32(binary.LittleEndian.Uint32(b))
	}
	if len(b) >= 8 {
		r.PortVerify, r.Direct, r.ServiceVerify, r.Reserved = b[4], b[5], b[6], b[7]
	}
	return r, nil
}
func (v LoginResult) Encode() []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b, uint32(v.Result))
	b[4] = v.PortVerify
	b[5] = v.Direct
	b[6] = v.ServiceVerify
	b[7] = v.Reserved
	return b
}

type Setup struct {
	Port    uint16
	Address [4]byte
	Service string
	Legacy  bool
}

// ParseSetup reads the network-order port and bounded, terminated service name:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#setup-01
func ParseSetup(b []byte) (Setup, error) {
	if (len(b) != 16 && len(b) < 52) || len(b) > MaxData {
		return Setup{}, ErrFrame
	}
	v := Setup{Port: binary.BigEndian.Uint16(b[2:]), Legacy: len(b) == 16}
	copy(v.Address[:], b[4:8])
	// The native receiver overwrites family with AF_INET. Only the supplied
	// IPv4/port and terminated service field are consumed; suffix is opaque.
	if len(b) >= 52 {
		i := bytes.IndexByte(b[16:49], 0)
		if i < 0 {
			return Setup{}, ErrFrame
		}
		v.Service = string(b[16 : 16+i])
	}
	return v, nil
}
func validService(s string) bool { return len(s) <= 32 && bytes.IndexByte([]byte(s), 0) < 0 }
func (v Setup) Encode() ([]byte, error) {
	if !validService(v.Service) || v.Legacy && v.Service != "" {
		return nil, ErrFrame
	}
	n := 52
	if v.Legacy {
		n = 16
	}
	b := make([]byte, n)
	binary.LittleEndian.PutUint16(b, 2)
	binary.BigEndian.PutUint16(b[2:], v.Port)
	copy(b[4:8], v.Address[:])
	if !v.Legacy {
		copy(b[16:49], v.Service)
	}
	return b, nil
}

type PortCheck struct {
	ID, Port        uint16
	Valid, Reserved byte
}

// ParsePort reads the little-endian correlation ID and port:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#port-and-service-verification
func ParsePort(b []byte) (PortCheck, error) {
	if len(b) != 6 {
		return PortCheck{}, ErrFrame
	}
	return PortCheck{binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint16(b[2:]), b[4], b[5]}, nil
}
func (v PortCheck) Encode() []byte {
	b := make([]byte, 6)
	binary.LittleEndian.PutUint16(b, v.ID)
	binary.LittleEndian.PutUint16(b[2:], v.Port)
	if v.Valid != 0 {
		b[4] = 1
	}
	return b
}

type ServiceCheck struct {
	ID      uint16
	Service string
	Valid   byte
}

// ParseService requires a terminated service name within the fixed-width field:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#port-and-service-verification
func ParseService(b []byte) (ServiceCheck, error) {
	if len(b) != 36 {
		return ServiceCheck{}, ErrFrame
	}
	i := bytes.IndexByte(b[2:35], 0)
	if i < 0 {
		return ServiceCheck{}, ErrFrame
	}
	return ServiceCheck{binary.LittleEndian.Uint16(b), string(b[2 : 2+i]), b[35]}, nil
}
func (v ServiceCheck) Encode() ([]byte, error) {
	if !validService(v.Service) {
		return nil, ErrFrame
	}
	b := make([]byte, 36)
	binary.LittleEndian.PutUint16(b, v.ID)
	copy(b[2:35], v.Service)
	if v.Valid != 0 {
		b[35] = 1
	}
	return b, nil
}
