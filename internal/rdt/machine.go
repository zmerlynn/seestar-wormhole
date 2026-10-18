package rdt

import (
	"errors"
	"slices"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// Config sets local resource policy. IDs are supplied by the session owner; it
// must allocate 1..254 cyclically and retain the previous remote ID on reuse.
// IOTCVersion selects the creation receipt rule in the Creation state
// machine. Start is valid only after the authenticated packet transport is ready.
type Config struct {
	SendCapacity                                       int // zero: immediate handoff; positive: outstanding Send actions until SendComplete
	LocalID, PreviousRemoteID, IOTCVersion             byte
	MaxPayload, TXBytes, TXPackets, RXBytes, RXPackets int
	InitialWindow, MinimumWindow                       int
	CreateTimeout, CloseTimeout                        time.Duration
}

// DefaultConfig uses the protocol's 1280-byte DATA, 1000/80 window and 30s creation
// values. Queue budgets and the 5s close bound are local policy, not wire credit.
// DATA payload limit:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/framing.md#size-limits
// Initial and minimum send window:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#window-and-pacing
// Creation phases and their timeout:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#creation
func DefaultConfig() Config {
	return Config{LocalID: 1, IOTCVersion: 6, MaxPayload: MaxData, TXBytes: 1 << 20, TXPackets: 1024, RXBytes: 4 << 20, RXPackets: 4096, InitialWindow: 1000, MinimumWindow: 80, CreateTimeout: 30 * time.Second, CloseTimeout: 5 * time.Second}
}

// Status is the local connection lifecycle.
type Status byte

const (
	Idle Status = iota
	Creating
	Established
	Closing
	Stopped
)

// EventKind identifies an owner input.
type EventKind byte

const (
	Start EventKind = iota
	Receive
	Write
	WriteUrgent
	Read
	Tick
	Close
	Cancel
	LocalAbort
	TransportFailed
	SendComplete
)

// Event borrows Packet.Payload and Data only during Step. Receive must already
// be authenticated; the machine validates Packet fields even after codec use.
// Write accepts a prefix of at most 8196 bytes and returns Accepted with Token
// and Count. WriteUrgent accepts at most MaxPayload bytes. Read is a
// one-shot demand of 1..8196 bytes; retry on Readable, never busy-poll an empty
// result. Tokens are echoed, not retained. Tick may be stale. A failed datagram
// adapter must deliver TransportFailed; Send means an atomic datagram handoff.
type Event struct {
	Kind   EventKind
	Packet Packet
	Data   []byte
	Count  int
	Token  uint64
}

// ActionKind identifies a nonblocking effect for the owner.
type ActionKind byte

const (
	Send ActionKind = iota
	Accepted
	Bytes
	Readable
	WriteCredit
	QueueStatus
	Ready
	End
	Closed
	Failed
	ArmTimer
	CancelTimer
)

// Action owns Data. Count is accepted/delivered bytes or current write credit;
// Usage is an immutable queue snapshot. ArmTimer replaces the owner's one timer.
// End follows exhaustion of preserved receive bytes after orderly peer close.
// Closed/Failed terminate network ownership; Read remains valid after Closed.
type Action struct {
	Kind   ActionKind
	Data   []byte
	Count  int
	Token  uint64
	At     core.Instant
	Usage  Usage
	Reason string
}

// Usage measures live payload storage, including partially consumed packets.
// Slice metadata is bounded by configured packet limits. Adapter/inbox storage
// is separate and must be bounded by the composing owner.
type Usage struct{ TXBytes, TXPackets, RXBytes, RXPackets, RXCapacity int }

type txPacket struct {
	packet  Packet
	sent    bool
	retried bool
	force   byte
	last    core.Instant
	window  int
}
type rxPacket struct {
	sequence Sequence
	data     []byte
}

// Machine is a pure single-owner RDT core. Construct with New; it uses no I/O,
// goroutines or wall clock. Timers and all elapsed intervals are monotonic.
type Machine struct {
	discards                                   [9]uint64 // cumulative R087-R095
	controls                                   []Packet  // at most one deferred unreliable control packet per type
	sends                                      int
	cfg                                        Config
	status                                     Status
	now, heartbeatAt, createAt, closeAt, timer core.Instant
	timerArmed                                 bool
	remote, peerVersion                        byte
	gotHeartbeat, gotHeartbeatACK              bool
	watchdog                                   int
	tx                                         []txPacket
	rx                                         []rxPacket
	txBytes, rxBytes                           int
	nextTX, nextRead, ackNext, lastContiguous  Sequence
	cumulative                                 bool
	cumulativeHeader                           Packet
	selective                                  bool
	selectiveRange                             Range
	selectiveHeader                            Packet
	ackAt, lastACK                             core.Instant
	rto, srtt, rttvar                          time.Duration
	window, lossWindow                         int
	lossAt                                     core.Instant
	hasLoss                                    bool
	end, endSent                               bool
	actions                                    []Action
}

// New validates explicit finite resource bounds before allocating queues.
func New(c Config) (*Machine, error) {
	if c.SendCapacity < 0 || c.SendCapacity > 65536 || c.LocalID < 1 || c.LocalID > 254 || c.PreviousRemoteID > 254 || c.MaxPayload < 100 || c.MaxPayload > MaxData || c.TXBytes < 1 || c.TXBytes > 64<<20 || c.RXBytes < 1 || c.RXBytes > 64<<20 || c.TXPackets < 1 || c.TXPackets > 65536 || c.RXPackets < 1 || c.RXPackets > 65536 || c.InitialWindow < 1 || c.InitialWindow > 6500 || c.MinimumWindow < 1 || c.MinimumWindow >= 6500 || c.CreateTimeout < 0 || c.CloseTimeout <= 0 {
		return nil, errors.New("invalid RDT configuration")
	}
	m := &Machine{cfg: c, rto: 500 * time.Millisecond, window: c.InitialWindow}
	return m, nil
}

// Status reports the local lifecycle.
func (m *Machine) Status() Status { return m.status }

// RemoteID is the learned ID to preserve when a session slot is reused.
func (m *Machine) RemoteID() byte { return m.remote }

// Usage reports live owned queue memory.
func (m *Machine) Usage() Usage {
	u := Usage{TXBytes: m.txBytes, TXPackets: len(m.tx), RXBytes: m.rxBytes, RXPackets: len(m.rx)}
	for _, p := range m.rx {
		u.RXCapacity += cap(p.data)
	}
	return u
}

// Credit is available normal-write capacity; each Write accepts at most 8196 bytes.
func (m *Machine) Credit() int {
	if m.status != Established {
		return 0
	}
	return min(m.cfg.TXBytes-m.txBytes, (m.cfg.TXPackets-len(m.tx))*m.cfg.MaxPayload)
}

// RTO reports the current retransmission interval for diagnostics.
func (m *Machine) RTO() time.Duration { return m.rto }

// Step consumes one event, clamps regressing time and returns owned effects.
// Resource work is bounded by the configured queues and one bounded read/write.
func (m *Machine) Step(e Event, now core.Instant) []Action {
	if e.Kind == SendComplete && m.sends > 0 {
		m.sends--
	}
	if now < m.now {
		m.discards[1]++
	}
	m.now = max(m.now, now)
	m.actions = nil
	if m.now == core.Instant(1<<63-1) && m.status != Stopped {
		m.stop("monotonic epoch exhausted", false)
		m.arm()
		out := m.actions
		m.actions = nil
		return out
	}
	before, credit, readable := m.Usage(), m.Credit(), m.readable()
	for len(m.controls) > 0 && m.sendAvailable() {
		p := m.controls[0]
		m.controls[0] = Packet{}
		m.controls = m.controls[1:]
		m.send(p)
	}
	// Expiration wins over a late packet. Stale timer callbacks cannot move time
	// backwards or reschedule relative to their obsolete deadline.
	m.timers()
	switch e.Kind {
	case Start:
		if m.status == Idle {
			m.status = Creating
			m.heartbeatAt = after(m.now, 100*time.Millisecond)
			if m.cfg.CreateTimeout > 0 {
				m.createAt = after(m.now, m.cfg.CreateTimeout)
			}
		}
	case Receive:
		if m.status != Idle && m.status != Stopped {
			m.receive(e.Packet)
		} else {
			m.discards[0]++
		}
	case Write, WriteUrgent:
		m.write(e)
	case Read:
		m.read(e)
	case Close:
		if m.status == Established {
			m.status = Closing
			m.watchdog = min(m.watchdog, 5)
			m.closeAt = after(m.now, m.cfg.CloseTimeout)
			// FIN occupies one extra fixed control slot, never payload budget.
			m.tx = append(m.tx, txPacket{packet: Packet{Type: FIN, Version: Version, ID: m.remote, Sequence: Sequence{0xffffffff, 0xffffffff}}})
		} else if m.status == Creating {
			m.control(Exit)
			m.stop("creation canceled", false)
		}
	case Cancel:
		if m.status == Creating {
			m.control(Exit)
			m.stop("creation canceled", false)
		} else if m.status == Established || m.status == Closing {
			m.control(Abort)
			m.stop("canceled", false)
		}
	case LocalAbort:
		if m.status == Creating || m.status == Established || m.status == Closing {
			m.control(Abort)
			m.stop("local abort", false)
		}
	case TransportFailed:
		if m.status != Stopped && m.status != Idle {
			m.stop("transport failed", false)
		}
	}
	if m.status == Established || m.status == Closing {
		m.pump()
	}
	if u := m.Usage(); u != before {
		m.emit(Action{Kind: QueueStatus, Usage: u})
	}
	if c := m.Credit(); c != credit {
		m.emit(Action{Kind: WriteCredit, Count: c})
	}
	if (!readable || e.Kind == Read) && m.readable() {
		m.emit(Action{Kind: Readable})
	}
	m.emitEnd()
	m.arm()
	out := m.actions
	m.actions = nil
	return out
}
func (m *Machine) emit(a Action) { m.actions = append(m.actions, a) }

// omitsACK distinguishes lost receipt information from ordinary aggregation.
// A newer cumulative watermark covers the old one; SACK ranges may cover an
// old range together even when no single new range contains it.
func omitsACK(old, next Packet) bool {
	switch old.Type {
	case DataACK, UrgentACK:
		return old.Sequence != next.Sequence
	case ACK2:
		return next.Sequence.Less(old.Sequence)
	case SACK2:
		prior, _ := DecodeSACK(old.Payload)
		current, _ := DecodeSACK(next.Payload)
		for _, r := range prior.Entries[:prior.Count] {
			start, covered := r.Start, false
			for range current.Count {
				for _, c := range current.Entries[:current.Count] {
					if start.Less(c.Start) || c.End.Less(start) {
						continue
					}
					if !c.End.Less(r.End) {
						covered = true
						break
					}
					start = c.End.Next()
				}
				if covered {
					break
				}
			}
			if !covered {
				return true
			}
		}
	}
	return false
}

func (m *Machine) send(p Packet) {
	if !m.sendAvailable() {
		// ACK/control retransmissions are unreliable and safely coalesce by type.
		// DATA/FIN remain in tx: pump never calls send without a reserved slot.
		for i, old := range m.controls {
			if old.Type == p.Type {
				if omitsACK(old, p) {
					m.discards[1]++
				}
				m.controls[i] = p
				return
			}
		}
		m.controls = append(m.controls, p)
		return
	}
	if m.cfg.SendCapacity > 0 {
		m.sends++
	}
	b, err := p.Encode()
	if err != nil {
		panic("invalid internally constructed RDT packet")
	}
	m.emit(Action{Kind: Send, Data: b})
}
func (m *Machine) sendAvailable() bool {
	return m.cfg.SendCapacity == 0 || m.sends < m.cfg.SendCapacity
}
func (m *Machine) control(t Type) {
	id := m.remote
	if t == Heartbeat {
		id = m.cfg.LocalID
	}
	m.send(Packet{Type: t, Version: Version, ID: id})
}
func (m *Machine) ack(p Packet, t Type) {
	p.Type = t
	p.Version = Version
	p.Payload = nil
	if t != HeartbeatACK {
		p.ID = m.remote
	}
	m.send(p)
}
func after(t core.Instant, d time.Duration) core.Instant {
	if core.Instant(d) > core.Instant(1<<63-1)-t {
		return core.Instant(1<<63 - 1)
	}
	return t.Add(d)
}
func (m *Machine) refresh() {
	m.watchdog = 120
	if m.status == Closing {
		m.watchdog = 5
	}
}

// receive implements connection and channel identity and complete
// observed message catalog. Zero-ID lookup remains supported, but a previous
// remote heartbeat cannot establish a fresh instance (conservative reuse rule).
// Destination identity, peer learning and creation rules:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#connection-identity
func (m *Machine) receive(p Packet) {
	if !receiveValid(p.Type, p.Payload) {
		m.discards[2]++
		return
	}
	if !known(p.Type) {
		m.discards[3]++
		return
	}
	if p.Type == Heartbeat {
		if p.Version > 3 {
			if m.remote == 0 {
				if p.ID == 0 || p.ID == m.cfg.PreviousRemoteID {
					m.discards[4]++
					return
				}
				m.remote = p.ID
			} else if p.ID != m.remote {
				m.discards[4]++
				return
			}
		}
		m.peerVersion = p.Version
		m.gotHeartbeat = true
		m.refresh()
		m.ack(p, HeartbeatACK)
		m.establish()
		return
	}
	if p.ID != 0 && p.ID != m.cfg.LocalID {
		m.discards[4]++
		return
	}
	switch p.Type {
	case HeartbeatACK:
		m.gotHeartbeatACK = true
		m.refresh()
		m.establish()
	case Data, Urgent:
		if m.status == Established || m.status == Closing {
			m.refresh()
			m.data(p)
		} else {
			m.discards[0]++
		}
	case DataACK, UrgentACK, ACK2, SACK2:
		if m.status == Established || m.status == Closing {
			m.refresh()
			m.acknowledged(p)
		} else {
			m.discards[0]++
		}
	case FIN:
		if (m.status == Established || m.status == Closing) && p.Sequence == (Sequence{0xffffffff, 0xffffffff}) {
			m.ack(p, FINACK)
			m.stop("", true)
		} else {
			m.discards[0]++
		}
	case FINACK:
		if m.status == Closing && p.Sequence == (Sequence{0xffffffff, 0xffffffff}) && len(m.tx) == 1 && m.tx[0].packet.Type == FIN && m.tx[0].sent {
			m.stop("", true)
		} else {
			m.discards[0]++
		}
	case Quit:
		// QUIT force-stops
		// even after failed delivery. Only FIN/FIN_ACK proves orderly close;
		// those paths already stop the machine before any later QUIT.
		m.stop("remote quit without orderly close", false)
	case Abort:
		m.ack(p, AbortACK)
		m.stop("remote abort", false)
	case AbortACK:
		m.stop("abort acknowledged", false)
	case Exit:
		m.stop("remote exit", false)
	case NeedDTLS:
		m.stop("peer requires DTLS", false)
	}
}

// Creation requires the heartbeat receipts selected by IOTC version:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#creation
func (m *Machine) establish() {
	if m.status != Creating {
		return
	}
	if m.cfg.IOTCVersion >= 6 && (!m.gotHeartbeat || !m.gotHeartbeatACK) {
		return
	}
	if m.watchdog == 0 {
		return
	}
	m.status = Established
	m.heartbeatAt = after(m.now, time.Second)
	m.emit(Action{Kind: Ready})
}

// Local acceptance is not delivery; immediate tail flush and finite budgets are local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#writing
func (m *Machine) write(e Event) {
	n := min(len(e.Data), m.Credit(), 8196)
	if e.Kind == WriteUrgent {
		n = min(n, m.cfg.MaxPayload)
	}
	for offset := 0; offset < n; {
		size := min(m.cfg.MaxPayload, n-offset)
		p := Packet{Type: Data, Version: Version, ID: m.remote, Sequence: m.nextTX, Payload: bytesCopy(e.Data[offset : offset+size])}
		if e.Kind == WriteUrgent {
			p.Type = Urgent
		}
		m.nextTX = m.nextTX.Next()
		node := txPacket{packet: p}
		m.tx = append(m.tx, node)
		m.txBytes += size
		offset += size
	}
	m.emit(Action{Kind: Accepted, Token: e.Token, Count: n})
}
func bytesCopy(b []byte) []byte   { out := make([]byte, len(b)); copy(out, b); return out }
func (m *Machine) readable() bool { return len(m.rx) > 0 && m.rx[0].sequence == m.nextRead }
func (m *Machine) read(e Event) {
	if e.Count < 1 || e.Count > 8196 {
		return
	}
	var out []byte
	for len(out) < e.Count && m.readable() {
		p := &m.rx[0]
		n := min(e.Count-len(out), len(p.data))
		out = append(out, p.data[:n]...)
		m.rxBytes -= n
		if n == len(p.data) {
			m.nextRead = m.nextRead.Next()
			m.rx = slices.Delete(m.rx, 0, 1)
		} else {
			p.data = bytesCopy(p.data[n:])
		}
	}
	if len(out) > 0 {
		m.emit(Action{Kind: Bytes, Token: e.Token, Count: len(out), Data: out})
	}
}
func (m *Machine) emitEnd() {
	if m.end && !m.endSent && len(m.rx) == 0 {
		m.endSent = true
		m.emit(Action{Kind: End})
	}
}

// data bounds storage before acknowledging new bytes. The protocol provides no
// peer receive credit; rejecting an unacknowledged datagram is
// recoverable loss, not a session failure. Never discard bytes already ACKed.
// Capacity is checked before ACK; unacknowledged loss and missing-head reserve are local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#receiving-and-reading
func (m *Machine) data(p Packet) {
	if p.Sequence.Low > 0xf0000000 {
		m.discards[5]++
		return
	}
	i, found := slices.BinarySearchFunc(m.rx, p.Sequence, func(p rxPacket, s Sequence) int {
		if p.sequence == s {
			return 0
		}
		if p.sequence.Less(s) {
			return -1
		}
		return 1
	})
	if found || p.Sequence.Less(m.nextRead) {
		m.discards[6]++
	}
	if !found && !p.Sequence.Less(m.nextRead) {
		bytes, slots := m.cfg.RXBytes-m.rxBytes, m.cfg.RXPackets-len(m.rx)
		// Selectively ACKed later packets cannot be evicted. Reserve for
		// ackNext even when nextRead is present: a short/partially read
		// prefix may free less than a full missing packet when drained.
		// Filling the gap can reclaim this reservation through ordered reads.
		if m.ackNext.Less(p.Sequence) {
			bytes -= min(MaxData, m.cfg.RXBytes)
			slots--
		}
		if slots < 1 || len(p.Payload) > bytes {
			m.discards[7]++
			return // No aggregate/single ACK may claim receipt of this packet.
		}
		node := rxPacket{sequence: p.Sequence, data: bytesCopy(p.Payload)}
		m.rx = slices.Insert(m.rx, i, node)
		m.rxBytes += len(p.Payload)
	}
	old := m.ackNext
	if p.Sequence == m.ackNext {
		m.lastContiguous = p.Sequence
		m.ackNext = p.Sequence.Next()
		for _, r := range m.rx {
			if r.sequence == m.ackNext {
				m.lastContiguous = r.sequence
				m.ackNext = r.sequence.Next()
			}
		}
	}
	if p.Type == Urgent {
		m.ack(p, UrgentACK)
		return
	}
	if m.peerVersion <= 4 {
		m.ack(p, DataACK)
		return
	}
	m.aggregate(p, old)
}

// aggregate follows receiver ACK generation. The
// 3ms threshold is input-driven; the replaceable timer provides 100ms fallback.
// Native ACK aggregation semantics with a locally scheduled fallback:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#ack-generation
func (m *Machine) aggregate(p Packet, old Sequence) {
	if m.ackAt == 0 {
		m.ackAt = after(m.now, 100*time.Millisecond)
	}
	if !old.Less(p.Sequence) {
		m.cumulative = true
		m.cumulativeHeader = p
		m.cumulativeHeader.Payload = nil
		if p.Sequence.Less(old) || m.now.Sub(m.lastACK) >= 3*time.Millisecond {
			m.sendCumulative()
			// The duplicate path increments pending progress after its immediate
			// ACK, so the send-loop fallback repeats it (Receiver generation).
			if p.Sequence.Less(old) {
				m.cumulative = true
			}
		}
		if m.selective {
			if !m.lastContiguous.Less(m.selectiveRange.End) {
				m.selective = false
			} else {
				if m.selectiveRange.Start.Less(m.lastContiguous) {
					m.selectiveRange.Start = m.lastContiguous
				}
				m.sendSelective()
			}
		}
	} else {
		if m.cumulative {
			m.sendCumulative()
		}
		if m.selective && m.selectiveRange.End.Next() == p.Sequence {
			m.selectiveRange.End = p.Sequence
			if m.now.Sub(m.lastACK) >= 3*time.Millisecond {
				m.sendSelective()
			}
		} else {
			if m.selective {
				m.sendSelective()
			}
			m.selective = true
			m.selectiveRange = Range{p.Sequence, p.Sequence}
			m.selectiveHeader = p
			m.selectiveHeader.Payload = nil
		}
	}
	if !m.cumulative && !m.selective {
		m.ackAt = 0
	}
}
func (m *Machine) sendCumulative() {
	p := m.cumulativeHeader
	p.Sequence = m.lastContiguous
	m.ack(p, ACK2)
	m.cumulative = false
	m.lastACK = m.now
}
func (m *Machine) sendSelective() {
	p := m.selectiveHeader
	p.Type = SACK2
	p.Version = Version
	p.ID = m.remote
	p.Sequence = m.lastContiguous
	p.Payload, _ = (SACK{Count: 1, Entries: []Range{m.selectiveRange}}).Encode()
	m.send(p)
	m.selective = false
	m.lastACK = m.now
}

// acknowledged applies exact/cumulative/inclusive selective semantics. Only
// transmitted DATA nodes are eligible; forged/future ACKs cannot free unsent
// bytes or FIN. SACK's header watermark never has cumulative meaning.
// Exact, inclusive cumulative and selective ACK boundaries:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#ack-processing-at-the-sender
func (m *Machine) acknowledged(p Packet) {
	var ranges []Range
	if p.Type == SACK2 {
		s, _ := DecodeSACK(p.Payload)
		ranges = s.Entries[:s.Count]
	}
	ignored, matched := false, false
	if p.Type == SACK2 {
		tail := p.Payload[8+16*len(ranges):]
		ignored = len(tail)%16 != 0
		for _, b := range tail {
			if b != 0 {
				ignored = true
				break
			}
		}
	}
	var sentMax Sequence
	hasSent := false
	sample := false
	var candidate txPacket
	kept := m.tx[:0]
	for _, node := range m.tx {
		remove, boundary, force := false, false, false
		s := node.packet.Sequence
		if !node.sent || node.packet.Type == FIN {
			switch p.Type {
			case DataACK, UrgentACK:
				if s == p.Sequence {
					ignored = true
				}
			case ACK2:
				if !p.Sequence.Less(s) {
					ignored = true
				}
			case SACK2:
				for _, r := range ranges {
					if !s.Less(r.Start) && !r.End.Less(s) {
						ignored = true
					}
				}
			}
		}
		if node.sent && node.packet.Type != FIN {
			if !hasSent || sentMax.Less(s) {
				sentMax = s
			}
			hasSent = true
			switch p.Type {
			case DataACK, UrgentACK:
				remove = s == p.Sequence
				boundary = remove
				force = p.Type == DataACK && m.peerVersion > 4 && s.Less(p.Sequence)
			case ACK2:
				remove = !p.Sequence.Less(s)
				boundary = s == p.Sequence
			case SACK2:
				for _, r := range ranges {
					if !s.Less(r.Start) && !r.End.Less(s) {
						remove = true
					}
					if s == r.End {
						boundary = true
					}
					if s.Less(r.Start) {
						force = true
					}
				}
			}
		}
		if remove {
			matched = true
			m.txBytes -= len(node.packet.Payload)
			if boundary {
				candidate = node
				sample = true
			}
		} else {
			if force && node.force == 0 {
				node.force = 1
			}
			kept = append(kept, node)
		}
	}
	clear(m.tx[len(kept):])
	m.tx = kept
	if p.Type == SACK2 {
		for _, r := range ranges {
			if !hasSent || sentMax.Less(r.End) {
				ignored = true
			}
		}
	} else if !hasSent || sentMax.Less(p.Sequence) {
		ignored = true
	}
	if ignored || !matched {
		m.discards[8]++
	}
	if sample {
		m.sample(candidate)
	}
}

// sample uses integer microseconds and the RTO equations. A wrapped attempt byte
// cannot re-enable RTT
// sampling after retransmission: retried remembers ambiguity for the node life.
// RTT/RTO arithmetic; retaining ambiguity across tx_count wrap is local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#rtt-and-rto
func (m *Machine) sample(n txPacket) {
	if n.retried || m.now < n.last {
		m.discards[8]++
		return
	}
	r := m.now.Sub(n.last) / time.Microsecond
	old := m.srtt / time.Microsecond
	v := m.rttvar / time.Microsecond
	if old == 0 {
		old = r
		v = r / 2
	} else {
		d := old - r
		if d < 0 {
			d = -d
		}
		v = (750*v + 250*d) / 1000
		old = (875*old + 125*r) / 1000
	}
	m.srtt = old * time.Microsecond
	m.rttvar = v * time.Microsecond
	m.rto = min(time.Duration(8_000_000), old+max(100_000, 4*v)) * time.Microsecond
}

// pump is the protocol's bounded send sweep. Ordinary initial sends use the local
// window (without reproducing its append-path one-packet overshoot). Urgent
// initial sends bypass the window; urgent retries are paced at the 20ms minimum
// worker interval, preventing a zero-time event loop.
// Window-limited resend sweeps and once-per-sweep RTO backoff:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#retransmission
func (m *Machine) pump() {
	if m.hasLoss {
		m.window = windowAt(m.lossWindow, m.cfg.MinimumWindow, len(m.tx), m.now.Sub(m.lossAt))
	}
	lost := false
	for i := range m.tx {
		if !m.sendAvailable() {
			break
		}
		n := &m.tx[i]
		if n.packet.Type == FIN && len(m.tx) != 1 {
			continue
		}
		if i >= max(1, m.window) && !(n.packet.Type == Urgent && !n.sent) {
			continue
		}
		due := !n.sent || n.force == 1 || m.now.Sub(n.last) >= m.rto
		if n.packet.Type == Urgent && m.now.Sub(n.last) >= 20*time.Millisecond {
			due = true
		}
		if !due {
			continue
		}
		if n.sent {
			n.retried = true
			if !lost {
				m.lossWindow = n.window
				m.lossAt = m.now
				m.hasLoss = true
				lost = true
			}
		}
		n.packet.Transmissions++
		n.sent = true
		n.last = m.now
		n.window = m.window
		if n.force == 1 {
			n.force = 2
		}
		m.send(n.packet)
	}
	if lost {
		m.rto = min(8*time.Second, 2*m.rto)
		m.window = windowAt(m.lossWindow, m.cfg.MinimumWindow, len(m.tx), 0)
	}
}

// windowAt implements pacing and flow control. The cube
// root is a small integer search; clamping before conversion avoids overflow.
// Local congestion-window arithmetic; no receive window is advertised:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#window-and-pacing
func windowAt(wmax, minimum, queued int, elapsed time.Duration) int {
	k := 0
	for (k+1)*(k+1)*(k+1) <= 5*wmax {
		k++
	}
	delta := (elapsed/time.Microsecond - time.Duration(100000*k)) / 1000
	x := float64(delta) / 1000
	f := x*x*x + float64(wmax)
	target := 0
	if f > 6500 {
		return min(6500, queued+1)
	}
	if f > 0 {
		target = int(f)
	}
	if target <= queued+1 {
		return max(minimum, target)
	}
	return queued + 1
}

// The separate five-second close deadline replaces no proven native FIN bound:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#fin-retransmission
// Heartbeat cadence and watchdog are distinct from the local close deadline:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#keepalive-and-watchdog
func (m *Machine) timers() {
	if m.status == Idle || m.status == Stopped {
		return
	}
	if m.status == Creating && m.createAt != 0 && m.now >= m.createAt {
		m.stop("creation timeout", false)
		return
	}
	if m.status == Closing && m.now >= m.closeAt {
		m.stop("local close timeout", false)
		return
	}
	if m.now >= m.heartbeatAt {
		interval := 100 * time.Millisecond
		if m.status != Creating {
			interval = time.Second
			elapsed := int64(m.now.Sub(m.heartbeatAt)/time.Second) + 1
			if elapsed >= int64(m.watchdog) {
				m.stop("receive watchdog expired", false)
				return
			}
			m.watchdog -= int(elapsed)
		}
		m.control(Heartbeat)
		m.heartbeatAt = after(m.now, interval)
	}
	if m.ackAt != 0 && m.now >= m.ackAt {
		if m.cumulative {
			m.sendCumulative()
		}
		if m.selective {
			m.sendSelective()
		}
		m.ackAt = 0
	}
}
func (m *Machine) arm() {
	if m.status == Idle || m.status == Stopped {
		if m.timerArmed {
			m.emit(Action{Kind: CancelTimer})
			m.timerArmed = false
		}
		return
	}
	at := m.heartbeatAt
	if m.status == Creating && m.createAt != 0 {
		at = min(at, m.createAt)
	}
	if m.status == Closing {
		at = min(at, m.closeAt)
	}
	if m.ackAt != 0 {
		at = min(at, m.ackAt)
	}
	if m.sendAvailable() && (m.status == Established || m.status == Closing) {
		for i, n := range m.tx {
			if i >= max(1, m.window) || n.packet.Type == FIN && len(m.tx) != 1 || !n.sent {
				continue
			}
			d := m.rto
			if n.packet.Type == Urgent {
				d = 20 * time.Millisecond
			}
			at = min(at, after(n.last, d))
		}
	}
	if at <= m.now {
		at = after(m.now, time.Nanosecond)
	}
	if !m.timerArmed || m.timer != at {
		m.timer = at
		m.timerArmed = true
		m.emit(Action{Kind: ArmTimer, At: at})
	}
}

// Preserving ordered EOF and rejecting standalone QUIT are explicit local safety policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/session.md#close-abort-and-failure
func (m *Machine) stop(reason string, orderly bool) {
	if m.status == Stopped {
		return
	}
	if orderly && m.status == Creating {
		orderly = false
		reason = "peer stopped during creation"
	}
	m.control(Quit)
	m.status = Stopped
	m.tx = nil
	m.txBytes = 0
	m.cumulative = false
	m.selective = false
	m.ackAt = 0
	if orderly {
		// A FIN cannot validly bypass a missing packet; preserve only an ordered
		// receive queue. A gap is a failure, never silently reported as stream EOF.
		expected := m.nextRead
		for _, p := range m.rx {
			if p.sequence != expected {
				orderly = false
				reason = "peer closed with receive gap"
				break
			}
			expected = expected.Next()
		}
	}
	if orderly {
		m.end = true
		m.emit(Action{Kind: Closed})
	} else {
		m.rx = nil
		m.rxBytes = 0
		m.emit(Action{Kind: Failed, Reason: reason})
	}
}

// Discards returns cumulative per-rule counts for R087-R095. Protocol ACK and
// suppression behavior is unchanged when these counters advance.
func (m *Machine) Discards() [9]uint64 { return m.discards }
