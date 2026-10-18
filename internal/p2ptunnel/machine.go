package p2ptunnel

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"time"
)

// Role selects outbound Agent or the simulator/embedding Server endpoint.
type Role byte

const (
	Agent Role = iota
	Server
)

type Status byte

const (
	Idle Status = iota
	LoggingIn
	Active
	Stopped
)

// Config is local resource policy, not negotiated credit. Server allows only
// configured ports at loopback; service mapping and legacy auth are unsupported.
// The session must already have authenticated DTLS before Start is delivered.
type Config struct {
	Role                                               Role
	MaxChannels, ChannelBytes, SessionBytes, HighWater int
	TXBytes, TXFrames                                  int
	LoginTimeout, VerifyTimeout                        time.Duration
	AllowedPorts                                       []uint16
	VerifyPorts                                        bool
}

// DefaultChannelBytes allows a multi-MiB PAUSE threshold plus a rounded 4 MiB
// device RDT backlog, one MiB staging and a final read.
// At B=8 MiB this still leaves almost 3 MiB additional burst headroom.
// PAUSE governs future socket reads, not bytes already in RDT. This is a lazy local
// allocation ceiling, not negotiated credit or a guarantee for arbitrary peers.
// Post-PAUSE burst size is unresolved; this ceiling is local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#backlog-after-pause
const DefaultChannelBytes = 16 * 1024 * 1024

// receiveProgressGrace is local overload policy, not a peer timer. One second
// allows eight 8192-byte writes at the accepted 64KiB/s before calling a holder
// stalled. It is consulted only at aggregate pressure, never as an idle timeout.
const receiveProgressGrace = time.Second

func DefaultConfig() Config {
	return Config{Role: Agent, MaxChannels: 256, ChannelBytes: DefaultChannelBytes, SessionBytes: 64 * 1024 * 1024, HighWater: 4 * 1024 * 1024, TXBytes: 1024 * 1024, TXFrames: 512, LoginTimeout: 30 * time.Second, VerifyTimeout: 5 * time.Second}
}

// Event data is borrowed only for Step; retained bytes are copied. Generation
// guards local callbacks; Token matches the exact outstanding read/write action.
// Stream carries ordered bytes (<= MaxInput), never an unreliable datagram.
type Event struct {
	Kind                       EventKind
	Channel, Mapping           byte
	Generation, Token, Request uint64
	Data                       []byte
	Count                      int
	Port                       uint16
	Failed                     bool
}
type EventKind byte

const (
	Start EventKind = iota
	Stream
	Open
	ReadReady
	LocalData
	LocalEOF
	LocalWritten
	LocalWritable
	LocalWriteFailed
	LocalClose
	Connected
	Sent
	TransportWritable
	Tick
	Stop
	TransportFailed
	ReceiveQueueSize
	// CancelOpen retires verification for Request without allocating a channel.
	CancelOpen
)

type ActionKind byte

const (
	Send ActionKind = iota
	ReadLocal
	WriteLocal
	ShutdownWrite
	CloseLocal
	ConnectLocal
	Opened
	OpenFailed
	SessionReady
	SessionClosed
	ArmTimer
	CancelTimer
	// ReceiveOddity reports a tolerated receive form using only its command.
	ReceiveOddity
)

// Actions own their Data. Send and WriteLocal require a matching completion,
// including Count for partial acceptance. A zero completion waits for Writable.
// ArmTimer replaces the single owner timer; stale Tick events are harmless.
// CloseOutcome preserves CLOSE diagnostics without changing the wire release action.
// LocalEnd records whether our END preceded CLOSE; result 1 is also used for
// ordinary EOF after receiving END.
type CloseOutcome struct {
	HasResult bool
	Result    byte
	LocalEnd  bool
}

type Action struct {
	Rule                       string // fixed refusal rule on CloseLocal
	DiscardedBytes             int
	Err                        error // correlated local refusal; never contains payloads
	Kind                       ActionKind
	Channel                    byte
	Generation, Token, Request uint64
	Data                       []byte
	Port                       uint16
	At                         core.Instant
	Reason                     string
	PeerClose                  *CloseOutcome // non-nil only for a received CLOSE
	Temporary                  bool          // OpenFailed: local capacity may become available without redial
	Command                    Command       // ReceiveOddity only
}

const MaxInput = HeaderSize + MaxData

type channel struct {
	active, connecting                                     bool
	generation, request                                    uint64
	readEnd, writeEnd, endPending, sentPause, writeBlocked bool
	handedPause, wakePending                               bool
	readState                                              byte
	readToken, writeToken                                  uint64
	writeCount                                             int
	queue                                                  receiveQueue
	queuedAt, lastWrite                                    core.Instant
	freeAt                                                 core.Instant
}
type outbound struct {
	data   []byte
	offset int
}
type pendingOpen struct {
	request  uint64
	mapping  byte
	port, id uint16
	until    core.Instant
}

// Machine owns every channel, queue, parser and deadline. No field is shared with
// edge workers. Construct with New; call Step from exactly one owner.
type Machine struct {
	telemetry                                                *tunnelTelemetry
	cfg                                                      Config
	status                                                   Status
	now, epoch, loginUntil, timer                            core.Instant
	decoder                                                  Decoder
	channels                                                 [256]channel
	last                                                     [256]byte
	serial                                                   uint64
	active, rxBytes, rdtBytes, reservedBytes, reservedFrames int
	tx                                                       []outbound
	txBytes                                                  int
	sendToken                                                uint64
	sendCount                                                int
	sendBlocked                                              bool
	flowTX                                                   outbound
	flowID                                                   byte
	flowCommand                                              Command
	flowGeneration                                           uint64
	sendFlow                                                 bool
	flowCursor                                               byte
	preferTX                                                 bool
	flowWakeSuperseded                                       bool
	pending                                                  *pendingOpen
	verify                                                   bool
	correlation                                              uint16
	wakeBytes                                                int
	flowDirty                                                bool
	actions                                                  []Action
}

func New(c Config) (*Machine, error) {
	if c.Role > Server || c.MaxChannels < 1 || c.MaxChannels > 256 || c.ChannelBytes < MaxData || c.SessionBytes < c.ChannelBytes || c.SessionBytes > 64*1024*1024 || c.HighWater < 2 || c.HighWater >= c.ChannelBytes || c.TXBytes < 2*MaxInput || c.TXBytes > 64*1024*1024 || c.TXFrames < 8 || c.TXFrames > 65536 || c.LoginTimeout <= 0 || c.VerifyTimeout <= 0 || len(c.AllowedPorts) > 65536 {
		return nil, errors.New("invalid tunnel limits")
	}
	c.AllowedPorts = append([]uint16(nil), c.AllowedPorts...)
	return &Machine{cfg: c}, nil
}
func (m *Machine) Status() Status { return m.status }

// Usage includes retained outbound frames and reserved local reads. RXCapacity
// equals RXBytes because queue storage is allocated to exact length. Flow controls
// have their own ControlBytes/ControlFrames reserve (at most 257 four-byte
// frames); TXBytes/TXFrames and read reservations exclude that reserve.
type Usage struct{ Channels, RXBytes, RXCapacity, TXBytes, TXFrames, ReservedBytes, ReservedFrames, ParserBytes, ControlBytes, ControlFrames int }

func (m *Machine) Usage() Usage {
	u := Usage{Channels: m.active, RXBytes: m.rxBytes, TXBytes: m.txBytes, TXFrames: len(m.tx), ReservedBytes: m.reservedBytes, ReservedFrames: m.reservedFrames, ParserBytes: m.decoder.Buffered()}
	for i := range m.channels {
		u.RXCapacity += m.channels[i].queue.capacity()
		if m.pendingFlow(byte(i)) != 0 {
			u.ControlFrames++
		}
	}
	if len(m.flowTX.data) != 0 {
		u.ControlFrames++
	}
	u.ControlBytes = HeaderSize * u.ControlFrames
	return u
}

// ChannelState is an immutable diagnostic snapshot, never the queued contents.
type ChannelState struct {
	Active                                   bool
	Generation                               uint64
	ReadEnd, WriteEnd, EndPending, SentPause bool
	ReadState                                byte
	Queued                                   int
	FreeAt                                   core.Instant
}

func (m *Machine) Channel(id byte) ChannelState {
	c := &m.channels[id]
	return ChannelState{c.active, c.generation, c.readEnd, c.writeEnd, c.endPending, c.sentPause, c.readState, c.queue.size, c.freeAt}
}
func (m *Machine) next() uint64  { m.serial++; return m.serial }
func (m *Machine) emit(a Action) { m.actions = append(m.actions, a) }
func (m *Machine) local(k ActionKind, id byte) Action {
	return Action{Kind: k, Channel: id, Generation: m.channels[id].generation}
}
func (m *Machine) failOpen(request uint64, why string) {
	m.emit(Action{Kind: OpenFailed, Request: request, Reason: why})
}
func (m *Machine) phase() Phase {
	if m.status == LoggingIn {
		if m.cfg.Role == Server {
			return LoginRequest
		}
		return LoginResponse
	}
	return Established
}
func (m *Machine) queue(f Frame, phase Phase) bool {
	b, err := f.Encode(phase)
	if err != nil || len(b) > m.cfg.TXBytes-m.txBytes-m.reservedBytes || len(m.tx)+m.reservedFrames >= m.cfg.TXFrames {
		m.stop("outbound budget or encoding failure")
		return false
	}
	m.tx = append(m.tx, outbound{data: b})
	m.txBytes += len(b)
	return true
}

// Flow controls use fixed per-channel state, independent of the DATA budget.
// State transitions do not require retaining obsolete unsent controls. A
// pending PAUSE/RESUME pair cancels; WAKE
// opportunities coalesce and are subsumed by a new desired PAUSE or RESUME.
// A WAKE generated while its predecessor PAUSE is outstanding stays pending
// until that PAUSE is completely accepted. Selecting
// or retrying PAUSE must not consume its successor. Release/generation changes
// invalidate pending state; partial frames remain immutable.
// Coalescing unsent controls is local scheduling policy over these wire semantics:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#pause-resume-and-wake
func (m *Machine) control(cmd Command, id byte) {
	if cmd == PauseCommand || cmd == ResumeCommand {
		// A zero completion can retry only an opportunity from this pause epoch.
		// Positive completions still finish the immutable outstanding frame.
		if len(m.flowTX.data) > 0 && m.flowCommand == WakeCommand && m.flowID == id {
			m.flowWakeSuperseded = true
		}
		m.channels[id].wakePending = false
		if m.telemetry != nil {
			m.telemetryFlow(cmd, id, m.flowQueue(id))
		}
		return
	}
	if cmd == WakeCommand {
		c := &m.channels[id]
		retained := c.sentPause && !c.wakePending
		c.wakePending = c.sentPause
		if retained && m.telemetry != nil {
			m.telemetryFlow(cmd, id, m.flowQueue(id))
		}
		return
	}
	m.queue(Frame{Command: cmd, Channel: id}, Established)
}
func (m *Machine) closeFrame(id byte, reason byte) {
	m.queue(Frame{Command: CloseCommand, Channel: id, Payload: []byte{reason}}, Established)
}
func (m *Machine) release(id byte, why string) {
	m.releaseOutcome(id, why, nil)
}

// CLOSE diagnostics never alter release; local generations do not add wire fields:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#data_close-04-payload
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#release
func (m *Machine) releaseOutcome(id byte, why string, outcome *CloseOutcome) {
	c := &m.channels[id]
	if !c.active {
		return
	}
	m.telemetryEndPause(id)
	if m.telemetry != nil {
		m.telemetry.channels[id].DiscardedBytes += uint64(c.queue.size)
	}
	if m.telemetry != nil && why == "receive budget exceeded" && c.queue.size > 0 {
		m.telemetry.channels[id].Reclaims++
	}
	a := m.local(CloseLocal, id)
	a.Reason = why
	switch why {
	case "peer replaced setup":
		a.Rule = "R065"
	case "local write failed":
		a.Rule = "R069"
	}
	if a.Rule != "" {
		a.DiscardedBytes = c.queue.size
		a.Err = fmt.Errorf("%s: %s; discarded %d bytes", a.Rule, why, c.queue.size)
	}
	a.PeerClose = outcome
	m.emit(a)
	m.rxBytes -= c.queue.size
	m.flowDirty = true
	m.active--
	if c.readToken != 0 {
		m.reservedBytes -= MaxInput
		m.reservedFrames--
	}
	// Native state 2 becomes free on the 29th one-second sweep. Keep that
	// phase relative to Start, but use monotonic time rather than wall time.
	free := m.epoch.Add(time.Duration((m.now-m.epoch)/core.Instant(time.Second)+29) * time.Second)
	generation := c.generation
	*c = channel{generation: generation, freeAt: free}
}
func (m *Machine) stop(why string) {
	if m.status == Stopped {
		return
	}
	m.status = Stopped
	if m.pending != nil {
		m.failOpen(m.pending.request, why)
		m.pending = nil
	}
	for i := range m.channels {
		m.release(byte(i), why)
	}
	m.tx = nil
	m.flowTX = outbound{}
	m.txBytes = 0
	m.sendToken = 0
	m.sendCount = 0
	m.decoder.Reset()
	m.rdtBytes = 0
	m.wakeBytes = 0
	// Remove effects created earlier in this same Step that reference canceled
	// I/O; preserve close/error notifications only. Edge cancels older actions.
	kept := m.actions[:0]
	for _, a := range m.actions {
		if a.Kind == CloseLocal || a.Kind == OpenFailed {
			kept = append(kept, a)
		}
	}
	m.actions = kept
	m.emit(Action{Kind: SessionClosed, Reason: why})
}

// pendingFlow represents the latest desired state relative to the last
// completely accepted control. An in-flight frame stays immutable until its
// completion; zero acceptance permits cancellation, partial acceptance does not.
func (m *Machine) pendingFlow(id byte) Command {
	c := &m.channels[id]
	if !c.active {
		return 0
	}
	if c.sentPause != c.handedPause {
		if c.sentPause {
			return PauseCommand
		}
		return ResumeCommand
	}
	if c.sentPause && c.wakePending {
		return WakeCommand
	}
	return 0
}

// Never interleave a partially accepted frame; fair alternation is local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#frame-envelope
func (m *Machine) pumpSend() {
	if m.sendBlocked || m.sendToken != 0 || m.status == Stopped {
		return
	}
	// Never insert a control into a partially accepted DATA/lifecycle frame.
	partial := len(m.tx) > 0 && m.tx[0].offset > 0
	// Alternate complete controls with FIFO frames when both are available.
	// A cyclic channel scan bounds a continuously pending control to 256 flow
	// turns / 512 complete frames after an outstanding control (513 including
	// it). FIFO frame k needs at most 2*(k+1) complete
	// frames including an outstanding frame. These are local service bounds,
	// conditional on transport acceptance, not wall-clock delivery deadlines.
	if len(m.flowTX.data) == 0 && !partial && !(m.preferTX && len(m.tx) > 0) {
		for i := range m.channels {
			id := m.flowCursor + byte(i)
			cmd := m.pendingFlow(id)
			if cmd == 0 {
				continue
			}
			m.flowTX = outbound{data: []byte{byte(cmd), id, 0, 0}}
			m.flowID, m.flowCommand, m.flowGeneration = id, cmd, m.channels[id].generation
			if cmd == WakeCommand {
				m.flowWakeSuperseded = false
				m.channels[id].wakePending = false
			}
			break
		}
	}
	var b []byte
	m.sendFlow = len(m.flowTX.data) > 0
	if m.sendFlow {
		b = m.flowTX.data[m.flowTX.offset:]
	} else {
		if len(m.tx) == 0 {
			return
		}
		b = m.tx[0].data[m.tx[0].offset:]
	}
	m.sendToken = m.next()
	m.sendCount = len(b)
	m.emit(Action{Kind: Send, Token: m.sendToken, Data: bytes.Clone(b)})
}

// Drain received DATA before acting on END, retaining the reverse direction:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#state-flags
func (m *Machine) pumpLocal(id byte) {
	c := &m.channels[id]
	if !c.active || c.connecting || c.writeEnd || c.writeToken != 0 || c.writeBlocked {
		return
	}
	if c.queue.size > 0 {
		c.writeCount = min(c.queue.size, MaxData)
		c.writeToken = m.next()
		if m.telemetry != nil {
			m.telemetry.channels[id].writeAt = m.now
			m.telemetryChanged(id)
		}
		a := m.local(WriteLocal, id)
		a.Token = c.writeToken
		a.Data = c.queue.front()
		m.emit(a)
		return
	}
	if c.endPending {
		if c.readEnd {
			m.release(id, "both directions ended")
		} else {
			c.writeEnd = true
			m.emit(m.local(ShutdownWrite, id))
		}
	}
}

// flowQueue preserves the shared RDT pressure term, but attributes it
// proportionally to retained tunnel
// backlogs instead of charging it in full to every channel. This is local fair
// overload policy, not a claim about native accounting or per-channel RDT credit:
// unread/out-of-order RDT bytes cannot yet be demultiplexed. Empty channels get
// no share; when all are empty the owner drains RDT without pausing a producer.
// Floor rounding leaves less than one byte per holder unattributed. Config caps
// both factors at 64 MiB, so the product fits int64 even on a 32-bit host.
// This proportional attribution deliberately differs from the native shared-count formula:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#sending
func (m *Machine) flowQueue(id byte) int {
	queued := m.channels[id].queue.size
	if queued == 0 {
		return 0
	}
	return queued + int(int64(m.rdtBytes)*int64(queued)/int64(m.rxBytes))
}

// Strict high/low comparisons and WAKE read opportunities:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#pause-resume-and-wake
func (m *Machine) flow(id byte) {
	c := &m.channels[id]
	if !c.active {
		return
	}
	q := m.flowQueue(id)
	low := 4 * m.cfg.HighWater / 5
	if q > m.cfg.HighWater && !c.sentPause {
		c.sentPause = true
		m.control(PauseCommand, id)
	} else if q < low && c.sentPause {
		c.sentPause = false
		m.control(ResumeCommand, id)
	}
}
func (m *Machine) allowed(port uint16) bool {
	for _, p := range m.cfg.AllowedPorts {
		if p == port && port != 0 {
			return true
		}
	}
	return false
}
func (m *Machine) activate(id byte, request uint64, connecting bool) {
	m.channels[id] = channel{active: true, generation: m.next(), request: request, connecting: connecting}
	m.active++
	m.telemetryChanged(id)
}

// openRoom treats local output fullness as backpressure, before queue's fatal
// guard. No channel or verification ID is consumed until its frame fits.
func (m *Machine) openRoom(request uint64, size int) bool {
	if size > m.cfg.TXBytes-m.txBytes-m.reservedBytes || len(m.tx)+m.reservedFrames >= m.cfg.TXFrames {
		m.emit(Action{Kind: OpenFailed, Request: request, Reason: "open output busy", Temporary: true})
		return false
	}
	return true
}

// Nonzero verification ID, one pending request and native channel quarantine:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#agent-behaviour
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#channel-allocation-and-reuse
func (m *Machine) open(request uint64, mapping byte, port uint16) {
	if m.active >= m.cfg.MaxChannels {
		m.emit(Action{Kind: OpenFailed, Request: request, Reason: "channel limit", Temporary: true})
		return
	}
	last := m.last[mapping]
	for n := 1; n <= 255; n++ {
		id := byte(int(last) + n)
		c := &m.channels[id]
		if c.active || c.freeAt > m.now {
			continue
		}
		if !m.openRoom(request, HeaderSize+52) {
			return
		}
		m.last[mapping] = id
		body, _ := (Setup{Port: port, Address: [4]byte{127, 0, 0, 1}}).Encode()
		if !m.queue(Frame{Command: SetupCommand, Channel: id, Payload: body}, Established) {
			m.failOpen(request, "transport unavailable")
			return
		}
		m.activate(id, request, false)
		a := m.local(Opened, id)
		a.Request = request
		m.emit(a)
		return
	}
	m.emit(Action{Kind: OpenFailed, Request: request, Reason: "no free channel (reuse quarantine)", Temporary: true})
}

// Phase dispatch, login outcomes, verification and channel operations:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#message-catalogue
func (m *Machine) onFrame(f Frame) {
	if f.oddity(m.phase()) {
		m.emit(Action{Kind: ReceiveOddity, Command: f.Command})
	}
	if m.status == LoggingIn {
		if m.cfg.Role == Agent {
			if f.Command != PassCommand {
				m.stop("login rejected")
				return
			}
			r, _ := ParseLoginResult(f.Payload)
			m.verify = r.PortVerify != 0
		} else {
			l, _ := ParseLogin(f.Payload)
			if len(l.Auth) != 0 {
				m.stop("legacy authentication unsupported")
				return
			}
			r := LoginResult{}
			if m.cfg.VerifyPorts {
				r.PortVerify = 1
			}
			if !m.queue(Frame{Command: PassCommand, Payload: r.Encode()}, LoginResponse) {
				return
			}
		}
		m.status = Active
		m.loginUntil = 0
		m.emit(Action{Kind: SessionReady})
		return
	}
	c := &m.channels[f.Channel]
	switch f.Command {
	case PortResponse:
		p, _ := ParsePort(f.Payload)
		if m.pending == nil || p.ID != m.pending.id {
			return
		}
		pending := m.pending
		m.pending = nil
		if p.Port != pending.port || p.Valid == 0 {
			m.failOpen(pending.request, "port verification rejected or mismatched")
			return
		}
		m.open(pending.request, pending.mapping, pending.port)
	case PortRequest:
		if m.cfg.Role != Server {
			return
		}
		p, _ := ParsePort(f.Payload)
		p.Valid = 0
		p.Reserved = 0
		if m.allowed(p.Port) {
			p.Valid = 1
		}
		m.queue(Frame{Command: PortResponse, Payload: p.Encode()}, Established)
	case ServiceRequest:
		if m.cfg.Role != Server {
			return
		}
		s, _ := ParseService(f.Payload)
		s.Valid = 0
		b, _ := s.Encode()
		m.queue(Frame{Command: ServiceResponse, Payload: b}, Established)
	case ServiceResponse: // Optional mapping is deliberately not exposed.
	case SetupCommand:
		if m.cfg.Role != Server {
			m.stop("unexpected inbound setup")
			return
		}
		s, _ := ParseSetup(f.Payload)
		if s.Service != "" || s.Address != [4]byte{127, 0, 0, 1} || !m.allowed(s.Port) {
			m.closeFrame(f.Channel, 2)
			return
		}
		if c.active {
			m.release(f.Channel, "peer replaced setup")
		}
		if m.active >= m.cfg.MaxChannels {
			m.closeFrame(f.Channel, 1)
			return
		}
		m.activate(f.Channel, 0, true)
		a := m.local(ConnectLocal, f.Channel)
		a.Port = s.Port
		m.emit(a)
	case DataCommand:
		if !c.active || c.writeEnd {
			return
		}
		if len(f.Payload) > m.cfg.ChannelBytes-c.queue.size {
			m.telemetryReject(f.Channel)
			m.closeFrame(f.Channel, 1)
			m.release(f.Channel, "receive budget exceeded")
			return
		}
		if !m.receiveRoom(f.Channel, len(f.Payload)) {
			m.telemetryReject(f.Channel)
			return
		}
		if c.queue.size == 0 && len(f.Payload) > 0 {
			// An idle channel gets grace for each new backlog, regardless of
			// when its previous transfer finished. Further DATA cannot renew it.
			c.queuedAt = m.now
		}
		c.queue.push(f.Payload)
		m.rxBytes += len(f.Payload)
		m.flowDirty = true
		m.telemetryAdmission(f.Channel, len(f.Payload))
		m.flow(f.Channel)
		m.pumpLocal(f.Channel)
	case EndCommand:
		if c.active {
			c.endPending = true
			m.pumpLocal(f.Channel)
		}
	case CloseCommand:
		outcome := &CloseOutcome{LocalEnd: c.readEnd}
		if len(f.Payload) != 0 {
			outcome.HasResult = true
			outcome.Result = f.Payload[0]
		}
		m.releaseOutcome(f.Channel, "peer close", outcome)
	case PauseCommand:
		if c.active {
			c.readState += 2
			if m.telemetry != nil {
				m.telemetry.channels[f.Channel].PauseReceived++
				m.telemetryChanged(f.Channel)
			}
		}
	case ResumeCommand:
		if c.active {
			c.readState = 0
			if m.telemetry != nil {
				m.telemetry.channels[f.Channel].ResumeReceived++
				m.telemetryChanged(f.Channel)
			}
		}
	case WakeCommand:
		if c.active {
			c.readState = 2
			if m.telemetry != nil {
				m.telemetry.channels[f.Channel].WakeReceived++
				m.telemetryChanged(f.Channel)
			}
		}
	}
}

// receiveRoom reclaims stalled holders before any holder within progress grace.
// Within either class the largest retained backlog wins, ties by lowest ID.
// If all holders are within grace, each has the same fair share of SessionBytes;
// largest backlog is furthest above that share. Shedding it bounds admission work
// without waiting for local I/O or arbitrarily favoring ports/reconnects.
// Incoming bytes do not affect selection: empty recipients hold no storage.
// Each pass releases a positive backlog, so at most 256 fixed-array scans occur.
// The per-channel check runs first; this does not weaken either byte ceiling.
// Finite storage and progress-aware channel reclamation are local overload policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#backlog-after-pause
func (m *Machine) receiveRoom(id byte, count int) bool {
	for count > m.cfg.SessionBytes-m.rxBytes {
		victim, largest := byte(0), 0
		stalledVictim := false
		for i := range m.channels {
			c := &m.channels[i]
			if c.queue.size == 0 {
				continue
			}
			stalled := m.now >= max(c.queuedAt, c.lastWrite).Add(receiveProgressGrace)
			if largest == 0 || stalled && !stalledVictim || stalled == stalledVictim && c.queue.size > largest {
				victim, largest, stalledVictim = byte(i), c.queue.size, stalled
			}
		}
		m.closeFrame(victim, 1)
		m.release(victim, "receive budget exceeded")
		if victim == id || m.status == Stopped {
			return false
		}
	}
	return true
}

func (m *Machine) deadlines() {
	if m.status == LoggingIn && m.now >= m.loginUntil {
		m.stop("login timeout")
	}
	if m.pending != nil && m.now >= m.pending.until {
		m.emit(Action{Kind: OpenFailed, Request: m.pending.request, Reason: "verification timeout", Temporary: true})
		m.pending = nil
	}
}
func (m *Machine) arm() {
	var at core.Instant
	if m.status == LoggingIn {
		at = m.loginUntil
	}
	if m.pending != nil && (at == 0 || m.pending.until < at) {
		at = m.pending.until
	}
	if m.status == Active {
		for i := range m.channels {
			c := &m.channels[i]
			if !c.active && c.freeAt > m.now && (at == 0 || c.freeAt < at) {
				at = c.freeAt
			}
		}
	}
	if at != m.timer {
		m.timer = at
		if at == 0 {
			m.emit(Action{Kind: CancelTimer})
		} else {
			m.emit(Action{Kind: ArmTimer, At: at})
		}
	}
}

// Step processes one bounded event. A regressing injected clock is clamped; wall
// jumps must not shorten quarantine. Fatal transport/framing errors release all
// channels immediately and require a fresh Machine for a new session attempt.
func (m *Machine) Step(e Event, now core.Instant) []Action {
	m.actions = nil
	m.flowDirty = false
	m.now = max(m.now, now)
	m.deadlines()
	if m.status == Stopped {
		if e.Kind == Open {
			m.failOpen(e.Request, "session stopped")
		}
		m.arm()
		return m.actions
	}
	switch e.Kind {
	case Start:
		if m.status != Idle {
			break
		}
		m.status = LoggingIn
		m.epoch = m.now
		m.loginUntil = m.now.Add(m.cfg.LoginTimeout)
		if m.cfg.Role == Agent {
			b, _ := (Login{Version: AgentVersion}).Encode()
			m.queue(Frame{Command: LoginCommand, Payload: b}, LoginRequest)
		}
	case Stop, TransportFailed:
		m.stop("session stopped")
	case Stream:
		if m.status == Idle || len(e.Data) > MaxInput {
			m.stop("invalid stream event")
			break
		}
		data := e.Data
		for len(data) > 0 && m.status != Stopped {
			used, f, err := m.decoder.Feed(data, m.phase())
			if err != nil {
				m.stop("malformed tunnel frame")
				break
			}
			data = data[used:]
			if f != nil {
				m.onFrame(*f)
			}
		}
	case CancelOpen:
		if m.pending != nil && m.pending.request == e.Request {
			m.pending = nil
		}
	case Open:
		if m.status != Active || m.cfg.Role != Agent || e.Port == 0 {
			m.failOpen(e.Request, "session not ready or invalid port")
			break
		}
		if !m.verify {
			m.open(e.Request, e.Mapping, e.Port)
			break
		}
		if m.pending != nil {
			m.emit(Action{Kind: OpenFailed, Request: e.Request, Reason: "verification busy", Temporary: true})
			break
		}
		if !m.openRoom(e.Request, HeaderSize+6) {
			break
		}
		m.correlation++
		if m.correlation == 0 || m.correlation == 0xffff {
			m.correlation = 1
		}
		m.pending = &pendingOpen{e.Request, e.Mapping, e.Port, m.correlation, m.now.Add(m.cfg.VerifyTimeout)}
		m.queue(Frame{Command: PortRequest, Payload: (PortCheck{ID: m.correlation, Port: e.Port}).Encode()}, Established)
	case Sent:
		if m.sendToken == 0 || e.Token != m.sendToken {
			break
		}
		if e.Failed || e.Count < 0 || e.Count > m.sendCount {
			m.stop("RDT write failed")
			break
		}
		m.sendToken = 0
		m.sendCount = 0
		if e.Count > 0 {
			if m.sendFlow {
				m.flowTX.offset += e.Count
				if m.flowTX.offset == len(m.flowTX.data) {
					// Count the immutable frame even if release or slot reuse has
					// invalidated its effect on the current channel generation.
					m.telemetrySent(m.flowCommand, m.flowID, 0)
					c := &m.channels[m.flowID]
					if c.active && c.generation == m.flowGeneration {
						if m.flowCommand == PauseCommand {
							c.handedPause = true
						}
						if m.flowCommand == ResumeCommand {
							c.handedPause = false
						}
					}
					m.flowTX = outbound{}
					m.flowCursor = m.flowID + 1
					m.preferTX = true
				}
			} else {
				t := &m.tx[0]
				t.offset += e.Count
				// Retain and charge the whole frame until completely accepted.
				if t.offset == len(t.data) {
					m.preferTX = false
					m.telemetrySent(Command(t.data[0]), t.data[1], len(t.data)-HeaderSize)
					m.txBytes -= len(t.data)
					m.tx[0] = outbound{}
					m.tx = m.tx[1:]
					if len(m.tx) == 0 {
						m.tx = nil
					}
				}
			}
		} else {
			if m.sendFlow && m.flowTX.offset == 0 {
				// No bytes reached RDT: latest state may replace this handoff attempt.
				if m.flowCommand == WakeCommand {
					c := &m.channels[m.flowID]
					if !m.flowWakeSuperseded && c.active && c.generation == m.flowGeneration && c.sentPause && c.handedPause {
						c.wakePending = true
					}
				}
				m.flowTX = outbound{}
			}
			m.sendBlocked = true
		}
	case TransportWritable:
		m.sendBlocked = false
	case ReceiveQueueSize:
		if e.Count < 0 || e.Count > m.cfg.SessionBytes {
			m.stop("RDT receive accounting exceeds session policy")
			break
		}
		m.rdtBytes = e.Count
		m.flowDirty = true
	case Tick:
	default:
		c := &m.channels[e.Channel]
		if !c.active || e.Generation != c.generation {
			break
		}
		switch e.Kind {
		case Connected:
			if !c.connecting {
				break
			}
			if e.Failed {
				m.closeFrame(e.Channel, 1)
				m.release(e.Channel, "connect failed")
			} else {
				c.connecting = false
				m.pumpLocal(e.Channel)
			}
		case ReadReady:
			if c.connecting || c.readEnd || c.readToken != 0 || c.readState >= 3 || m.txBytes+m.reservedBytes+MaxInput > m.cfg.TXBytes-64 || len(m.tx)+m.reservedFrames+1 > m.cfg.TXFrames-4 {
				break
			}
			if c.readState != 1 {
				c.readState++
			}
			c.readToken = m.next()
			m.reservedBytes += MaxInput
			m.reservedFrames++
			a := m.local(ReadLocal, e.Channel)
			a.Token = c.readToken
			m.emit(a)
		case LocalData, LocalEOF:
			if c.readToken == 0 || e.Token != c.readToken {
				break
			}
			c.readToken = 0
			m.reservedBytes -= MaxInput
			m.reservedFrames--
			if e.Kind == LocalEOF {
				c.readEnd = true
				if c.writeEnd {
					m.closeFrame(e.Channel, 1)
					m.release(e.Channel, "local EOF after peer end")
				} else {
					m.control(EndCommand, e.Channel)
					m.pumpLocal(e.Channel)
				}
				break
			}
			if len(e.Data) > MaxData {
				m.closeFrame(e.Channel, 1)
				m.release(e.Channel, "oversize local read")
				break
			}
			if len(e.Data) > 0 {
				m.queue(Frame{Command: DataCommand, Channel: e.Channel, Payload: e.Data}, Established)
			}
		case LocalWritten:
			if c.writeToken == 0 || e.Token != c.writeToken {
				break
			}
			if e.Count < 0 || e.Count > c.writeCount {
				m.stop("invalid local write completion")
				break
			}
			c.writeToken = 0
			c.writeCount = 0
			m.telemetryChanged(e.Channel)
			if e.Count > 0 {
				c.lastWrite = m.now
				c.queue.consume(e.Count)
				if m.telemetry != nil {
					m.telemetry.channels[e.Channel].DeliveredBytes += uint64(e.Count)
				}
				m.rxBytes -= e.Count
				m.flowDirty = true
				m.wakeBytes = min(m.wakeBytes+e.Count, 8197)
				m.flow(e.Channel)
				if c.active && c.sentPause && m.flowQueue(e.Channel) > 4*m.cfg.HighWater/5 && m.wakeBytes > 8196 {
					m.control(WakeCommand, e.Channel)
					m.wakeBytes = 0
				}
				m.pumpLocal(e.Channel)
			} else {
				c.writeBlocked = true
				m.flow(e.Channel)
			}
		case LocalWritable:
			c.writeBlocked = false
			m.pumpLocal(e.Channel)
		case LocalWriteFailed:
			if c.writeToken == 0 || e.Token != c.writeToken {
				break
			}
			c.writeToken = 0
			c.writeCount = 0
			m.telemetryChanged(e.Channel)
			m.closeFrame(e.Channel, 1)
			m.release(e.Channel, "local write failed")
		case LocalClose:
			m.closeFrame(e.Channel, 1)
			m.release(e.Channel, "local close")
		}
	}
	// A holder growing, draining or being reclaimed changes every other
	// holder's shared-pressure share. Recheck once per bounded input event;
	// local completion/close alone must suffice to resume an empty producer.
	if m.flowDirty && m.status == Active {
		for i := range m.channels {
			m.flow(byte(i))
			if m.status == Stopped {
				break
			}
		}
	}
	m.pumpSend()
	m.arm()
	return m.actions
}

var _ core.Machine[Event, Action] = (*Machine)(nil)
