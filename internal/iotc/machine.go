package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"net/netip"
	"time"
)

// Transport identifies an edge-owned UDP socket or TCP connection.
type Transport byte

const (
	UDP Transport = iota
	TCP
)

// Link binds endpoint and transport to an attempt-local connection generation.
// UDP uses ID zero; TCP completions must echo the OpenTCP action's nonzero ID.
type Link struct {
	Transport Transport
	Address   netip.AddrPort
	ID        uint64
}

// Phase describes local ownership, never a vendor wire state.
type Phase byte

const (
	Idle Phase = iota
	Connecting
	Established
	Closed
)

// EventKind enumerates bounded owner inputs.
type EventKind byte

const (
	Start EventKind = iota
	Resolved
	MaterialReady
	Connected
	LinkFailed
	Derived
	Encrypted
	Decrypted
	Wire
	Write
	Sent // edge completion of Send, echoed Token; no wire meaning
	Tick
	Cancel
	EffectFailed
	CryptoRejected
	// AttemptFailed reports an owner-wide failure (for example, a stopped executor).
	// Operation errors use EffectFailed; transport write errors use LinkFailed.
	AttemptFailed
)

// ActionKind enumerates edge effects and upward notifications.
type ActionKind byte

const (
	Resolve ActionKind = iota
	Generate
	OpenTCP
	CloseLink
	Derive
	Seal
	Open
	Send
	Ready
	Received
	Failed
	Disposed
	ArmTimer
	CancelTimer
	WriteRejected
	RelayProgressChanged // secret-free snapshot, emitted once per relay milestone
	ReceiveOddity        // bounded category/opcode/type; no payload or identity
)

// CryptoMode specifies maintained-library edge operations. RSA is the protocol's
// raw public operation on a right-zero-padded 256-byte block. GCM is AES128,
// tag16, with exact supplied AAD and the cached IV (no nonce counter).
type CryptoMode byte

const (
	RawMasterRSA CryptoMode = iota
	AESGCM
)

// Material is generated once by the edge using crypto/rand and P-256. Handles
// belong only to this attempt; public DER occupies 91 bytes of the 92-byte slot.
// The edge must allocate distinct nonzero request nonces, a fresh RID and an
// independent random nonzero HelloSerial for every attempt (edge.HelloSerialStart).
type Material struct {
	HelloSerial           uint32
	RID                   [8]byte
	MasterNonce, P2PNonce uint16
	MasterKey             [16]byte
	MasterIV              [12]byte
	Public                [92]byte
	MasterRef, PrivateRef KeyRef
}

// Config selects the current revision-29 outbound profile. License must already
// be verified by the edge. No tunnel password or DTLS key belongs in this config.
type Config struct {
	Attempt      uint64
	RecordSID    uint32
	UID          [20]byte
	License      License
	Region       string
	LocalNAT     byte
	P2PPortCount byte // Enabled P2P try-port option count; zero selects normal task cadence.
}

// Event borrows Data. Crypto completion Token and Attempt must match the action.
// Decrypted is emitted ONLY after successful authentication by the edge. Marker
// is an injected local timeval for master-list metadata, not untrusted wire data.
type Event struct {
	Kind           EventKind
	Attempt, Token uint64
	Link           Link
	Addresses      []netip.Addr
	Material       Material
	Key            KeyRef
	IV             [12]byte
	Data           []byte
	Marker         Marker
}

// Action owns Data/AAD. The edge must never log these structures (they can carry
// secret master material). Derive uses Private + first91(Public), raw P-256 ECDH
// first16 as AES key and fresh random IV12; Derived returns opaque Key and IV.
// Seal/Open use Key with its stored IV. Dispose is attempt-wide, idempotent and
// also destroys late-created resources. Send rejection delivers LinkFailed.
// EffectFailed echoes the failed action's Token and Link; a retired action
// cannot fail the attempt. AttemptFailed is reserved for owner-wide failures.
type Action struct {
	Kind           ActionKind
	Attempt, Token uint64
	Link           Link
	Domain         string
	Data, AAD      []byte
	Mode           CryptoMode
	Key, Private   KeyRef
	Public         [92]byte
	At             core.Instant
	Reason         string
	Failures       FailureReason
	Progress       RelayProgress
	Opcode         uint16 // ReceiveOddity only
	Oddity         Oddity // ReceiveOddity only
	Type           byte   // ReceiveOddity only; record or alert trigger type
}

// endpointStage is local state, never a vendor wire value. Per-role paths:
// master: initial -> masterAnswered;
// UDP server: initial -> helloObserved -> precheckPending -> precheckReady;
// TCP server: initial -> precheckPending -> precheckReady;
// selected server: precheckReady -> rendezvousPending -> relayInfoPending -> relayInfoReady.
// relayRefused retires only relay work; terminal retires the whole endpoint.
// Retired owns no link or pending effects.
// UDP and TCP selection, recovery and ownership remain attempt-local.
type endpointStage byte

const (
	endpointInitial endpointStage = iota
	helloObserved
	precheckPending
	precheckReady
	rendezvousPending
	relayInfoPending
	relayInfoReady
	relayRefused // UDP direct negotiation still owns the endpoint
	masterAnswered
	punchRecovery                  // established direct path: authenticated repeated 030d only
	endpointTerminal endpointStage = 254
	endpointRetired  endpointStage = 255
)

type endpoint struct {
	link                 Link
	master               bool
	connected            bool
	port                 int
	stage                endpointStage
	failure              FailureReason
	cache                int
	sid                  uint32
	hello                uint32      // latest issued serial
	helloPrior           [119]uint32 // plus latest: 120 requests in the 60 s / 500 ms window
	helloCount           int
	last                 core.Instant
	lastDirect           core.Instant // independent of relay requests and responses
	directUntil          core.Instant
	directInterval       time.Duration
	directExpired        bool
	peerNAT              byte
	relayID              uint16 // allocation belongs to this endpoint, not the last response
	relayReady           bool   // authenticated allocation available to the selector
	infoRequested        bool
	relayRefusalObserved bool
}
type operation struct {
	token  uint64
	kind   ActionKind
	link   Link
	typ    byte
	sid    uint32
	opcode uint16
	aad    [116]byte
	aadN   int
	size   int
	marker Marker
}

// Machine is single-owner and single-use. It contains fixed endpoint/operation
// tables and no sockets, goroutines, clocks, random generators or crypto library.
type Machine struct {
	resolveToken, materialToken               uint64
	nat                                       NATTracker
	relaySelected                             Link
	relayPending                              Link
	cfg                                       Config
	domain                                    string
	key                                       [16]byte
	state                                     Phase
	progress                                  RelayProgress
	now, start, next, aliveAt, lastKnock      core.Instant
	material                                  Material
	hasMaterial, hasDNS, started, udpResponse bool
	masters                                   [4]netip.Addr
	masterCount                               int
	endpoints                                 [32]endpoint
	count                                     int
	cache                                     Cache
	ivs                                       [12][12]byte
	pending                                   [64]operation
	serial                                    uint64
	helloSerial                               uint32
	selected                                  Transport
	selection                                 bool
	path                                      Link
	relay                                     uint16
	direct                                    bool
	candidates                                [12]netip.AddrPort
	candidateFailed                           [12]bool         // tombstones last for this attempt
	candidateUntil                            [12]core.Instant // zero: ordinary task, bounded by attempt
	candidateCount                            int
	knockReply                                [12]bool
	alive                                     int
	sequence                                  uint16
	out                                       []Action
}

// New copies configuration; callers must use a new machine and new edge crypto
// context for every retry. Global/device SID ffff is excluded from this client.
func New(c Config) (*Machine, error) {
	if c.Attempt == 0 || c.RecordSID == 0xffff || c.UID == [20]byte{} || c.LocalNAT > 3 {
		return nil, ErrPacket
	}
	d, e := c.License.MasterDomain(c.Region)
	if e != nil {
		return nil, e
	}
	c.License.Standalone = nil
	return &Machine{cfg: c, domain: d, key: ScrambleKey(c.License.Mask)}, nil
}

// State returns the local ownership phase.
func (m *Machine) State() Phase  { return m.state }
func (m *Machine) emit(a Action) { a.Attempt = m.cfg.Attempt; m.out = append(m.out, a) }
func (m *Machine) token() uint64 { m.serial++; return m.serial }
func after(t core.Instant, d time.Duration) core.Instant {
	const max = core.Instant(1<<63 - 1)
	if t > max-core.Instant(d) {
		return max
	}
	return t + core.Instant(d)
}

// Step uses monotonic elapsed nanoseconds; regressions are clamped. Late ticks
// do bounded catch-up for alive counters, never bursts of queued retries.
func (m *Machine) Step(e Event, now core.Instant) []Action {
	if e.Attempt != m.cfg.Attempt || m.state == Closed {
		return nil
	}
	if now < 0 {
		now = 0
	}
	if now < m.now {
		now = m.now
	}
	m.now = now
	m.out = nil
	beforeRelay := m.progress.Phase
	if now == core.Instant(1<<63-1) {
		m.close("monotonic clock exhausted", false)
		out := m.out
		m.out = nil
		return out
	}
	// The local total-attempt budget is stricter than accumulated stage timers.
	// Equality expires. Every completion observes it even when Tick is queued last.
	if m.state == Connecting && m.now.Sub(m.start) >= 60*time.Second {
		m.failConnecting(m.pathFailures() | ConnectionTimeout)
	} else if e.Kind == Start {
		if m.started {
			return nil
		}
		m.started = true
		m.state = Connecting
		m.start = now
		m.next = after(now, 10*time.Millisecond)
		m.materialToken = m.token()
		m.resolveToken = m.token()
		m.emit(Action{Kind: Generate, Token: m.materialToken})
		m.emit(Action{Kind: Resolve, Token: m.resolveToken, Domain: m.domain})
	} else if !m.started {
		return nil
	} else {
		m.expireDirectTasks()
		m.handleEvent(e)
	}
	m.finishIfExhausted()
	if m.progress.Phase != beforeRelay {
		// Notify before effects so edge callbacks cannot overtake the milestone.
		a := Action{Kind: RelayProgressChanged, Attempt: m.cfg.Attempt, Progress: m.RelayProgress()}
		m.out = append([]Action{a}, m.out...)
	}
	if m.state != Closed {
		at := m.next
		if m.state == Established {
			at = m.aliveAt
		}
		m.emit(Action{Kind: ArmTimer, At: max(at, m.now)})
	}
	out := m.out
	m.out = nil
	return out
}

// handleEvent dispatches only admitted owner events. Step owns deadline and
// shutdown ordering; each handler owns its transition's correlation checks.
func (m *Machine) handleEvent(e Event) {
	switch e.Kind {
	case Cancel:
		m.close("canceled", true)
	case Resolved:
		m.resolved(e)
	case MaterialReady:
		m.materialReady(e)
	case Connected:
		m.connected(e)
	case LinkFailed:
		m.linkFailed(e.Link)
	case Derived:
		m.derived(e)
	case Encrypted, Decrypted:
		m.cryptoDone(e)
	case Wire:
		if len(e.Data) <= MaxPacket {
			m.wire(e)
		}
	case Write:
		if m.state == Established && len(e.Data) > 0 && len(e.Data) <= MaxData {
			m.sendData(e.Data)
			if len(m.out) > 0 && m.out[len(m.out)-1].Kind == Send {
				m.out[len(m.out)-1].Token = e.Token
			}
		} else {
			m.emit(Action{Kind: WriteRejected, Reason: "not ready or datagram size"})
		}
	case Tick:
		m.tick()
	case CryptoRejected:
		for i, p := range m.pending {
			if p.token == e.Token && e.Token != 0 && p.kind == Open {
				m.pending[i] = operation{}
				break
			}
		}
	case EffectFailed:
		m.effectFailed(e)
	case AttemptFailed:
		m.close("attempt executor failed", true)
	}
}

func (m *Machine) resolved(e Event) {
	if m.state == Connecting && e.Token != 0 && e.Token == m.resolveToken && !m.hasDNS && len(e.Addresses) > 0 && len(e.Addresses) <= 4 {
		for _, ip := range e.Addresses {
			if !ip.Is4() || ip.IsUnspecified() {
				continue
			}
			duplicate := false
			for _, old := range m.masters[:m.masterCount] {
				if old == ip {
					duplicate = true
				}
			}
			if !duplicate {
				m.masters[m.masterCount] = ip
				m.masterCount++
			}
		}
		m.hasDNS = m.masterCount > 0
		if m.hasDNS {
			m.resolveToken = 0
		}
		m.begin()
	}
}

func (m *Machine) materialReady(e Event) {
	if m.state == Connecting && e.Token != 0 && e.Token == m.materialToken && !m.hasMaterial && validMaterial(e.Material) {
		m.material = e.Material
		m.helloSerial = e.Material.HelloSerial - 1
		m.hasMaterial = true
		m.materialToken = 0
		m.begin()
	}
}

func (m *Machine) connected(e Event) {
	if i := m.find(e.Link); i >= 0 && m.state == Connecting {
		v := &m.endpoints[i]
		if !v.connected {
			v.connected = true
			if v.master {
				m.query(i)
			} else {
				m.prepare(i)
			}
		}
	}
}

// effectFailed accepts only errors for operations that still belong to this
// owner. Tokens are attempt-unique; Link also binds connection generation.
func (m *Machine) effectFailed(e Event) {
	if e.Token == 0 {
		return
	}
	if e.Link == (Link{}) {
		if e.Token == m.resolveToken || e.Token == m.materialToken {
			m.close("discovery effect failed", false)
			return
		}
		for _, entry := range m.cache.Entries[:m.cache.Count] {
			if entry.Pending == e.Token {
				m.close("key derivation failed", false)
				return
			}
		}
	}
	if i := m.find(e.Link); i >= 0 && !m.endpoints[i].connected && e.Link.ID == e.Token {
		m.linkFailed(e.Link)
		return
	}
	for _, p := range m.pending {
		if p.token == e.Token && p.link == e.Link {
			m.close("edge effect failed", true)
			return
		}
	}
}

func validMaterial(v Material) bool {
	return v.HelloSerial != 0 && v.RID != [8]byte{} && v.MasterNonce != 0 && v.P2PNonce != 0 && v.MasterNonce != v.P2PNonce && v.MasterKey != [16]byte{} && v.MasterIV != [12]byte{} && v.MasterRef != 0 && v.PrivateRef != 0 && v.MasterRef != v.PrivateRef && v.Public != [92]byte{}
}

// Fresh attempt state and bounded callbacks implement inferred isolation guidance:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#attempt-and-cache-lifetimes
func (m *Machine) begin() {
	if !m.hasDNS || !m.hasMaterial || m.count > 0 {
		return
	}
	for _, ip := range m.masters[:m.masterCount] {
		u := Link{Address: netip.AddrPortFrom(ip, 10240)}
		i := m.add(endpoint{link: u, master: true, connected: true})
		m.query(i)
		m.openTCP(ip, true, -1)
	}
}
func (m *Machine) add(v endpoint) int {
	if m.count == len(m.endpoints) {
		return -1
	}
	i := m.count
	m.count++
	m.endpoints[i] = v
	return i
}
func (m *Machine) find(l Link) int {
	for i := 0; i < m.count; i++ {
		if m.endpoints[i].stage < endpointTerminal && m.endpoints[i].link == l {
			return i
		}
	}
	return -1
}
func tcpPort(i int) uint16 { return [5]uint16{80, 443, 21047, 8080, 8000}[i] }

// These are raw protocol TCP ports, cycled on failed opens:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#endpoint-discovery
func (m *Machine) openTCP(ip netip.Addr, master bool, cache int) {
	l := Link{TCP, netip.AddrPortFrom(ip, 80), m.token()}
	if m.add(endpoint{link: l, master: master, cache: cache}) >= 0 {
		m.emit(Action{Kind: OpenTCP, Link: l, Token: l.ID})
	}
}

// Only the failed alternative retires; tombstones and capacity are local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#udptcp-race
func (m *Machine) linkFailed(l Link) {
	// Direct candidates are not rendezvous endpoints. The selected path owns
	// transport failure regardless of how it was discovered.
	if m.state == Established && l == m.path {
		m.close("transport failed", false)
		return
	}
	if l.Transport == UDP && l.ID == 0 {
		for j, addr := range m.candidates[:m.candidateCount] {
			if addr == l.Address {
				m.candidateFailed[j] = true
				m.knockReply[j] = false
			}
		}
	}
	i := m.find(l)
	if i < 0 {
		return
	}
	v := &m.endpoints[i]
	m.retireEndpoint(i)
	if l.Transport == TCP && v.port < 4 && !m.selection {
		v.port++
		v.link.Address = netip.AddrPortFrom(l.Address.Addr(), tcpPort(v.port))
		v.link.ID = m.token()
		v.connected = false
		v.stage = endpointInitial
		m.emit(Action{Kind: OpenTCP, Link: v.link, Token: v.link.ID})
	} else {
		v.failure = TransportFailure
	}
}

// retireEndpoint releases every selector reference before a link generation can
// be replaced.
func (m *Machine) retireEndpoint(i int) {
	v := &m.endpoints[i]
	if v.stage == endpointRetired {
		return
	}
	m.emit(Action{Kind: CloseLink, Link: v.link})
	m.clearPending(v.link)
	if m.relayPending == v.link {
		m.relayPending = Link{}
	}
	if m.relaySelected == v.link {
		m.relaySelected = Link{}
	}
	v.connected = false
	v.relayReady = false
	v.relayID = 0
	v.stage = endpointRetired
	m.retireUnusedDerivations()
}

// Cache entries are shared across UDP/TCP ports. Retire a pending derive only
// after its last discovery consumer loses ownership.
func (m *Machine) retireUnusedDerivations() {
	for i := 0; i < m.cache.Count; i++ {
		needed := false
		for _, v := range m.endpoints[:m.count] {
			needed = needed || !v.master && v.cache == i && v.stage < precheckReady
		}
		if !needed {
			m.cache.Entries[i].Pending = 0
		}
	}
}

func (m *Machine) pendingFor(l Link, kind ActionKind, op uint16) bool {
	for _, p := range m.pending {
		if p.token != 0 && p.link == l && p.kind == kind && p.opcode == op {
			return true
		}
	}
	return false
}
func (m *Machine) reserve(p operation) bool {
	for i := range m.pending {
		if m.pending[i].token == 0 {
			m.pending[i] = p
			return true
		}
	}
	return false
}
func (m *Machine) clearPending(l Link) {
	for i, p := range m.pending {
		if p.link == l {
			m.pending[i] = operation{}
		}
	}
}

// Modern master query fields and raw RSA envelope:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/master-record.md#master-query-record-type-1
func (m *Machine) query(i int) {
	if i < 0 {
		return
	}
	v := &m.endpoints[i]
	if m.pendingFor(v.link, Seal, 0x100b) {
		return
	}
	p := masterQuery(m.cfg.UID, m.cfg.License.Realm, m.material)
	b, _ := p.Marshal()
	plain := make([]byte, 256)
	copy(plain, b)
	h, _ := RecordHeader(1, m.cfg.RecordSID, 256)
	m.seal(v.link, 1, 0x100b, RawMasterRSA, 0, h, plain)
	v.last = m.now
}
func (m *Machine) seal(l Link, typ byte, op uint16, mode CryptoMode, key KeyRef, aad, plain []byte) {
	if m.pendingFor(l, Seal, op) {
		return
	}
	p := operation{token: m.token(), kind: Seal, link: l, typ: typ, opcode: op, aadN: len(aad), size: len(plain)}
	copy(p.aad[:], aad)
	if !m.reserve(p) {
		return
	}
	m.emit(Action{Kind: Seal, Token: p.token, Link: l, Mode: mode, Key: key, AAD: append([]byte(nil), aad...), Data: plain})
}

// P-256 server agreement and cached material:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/master-record.md#server-key-agreement
func (m *Machine) prepare(i int) {
	if m.state != Connecting {
		return
	}
	v := &m.endpoints[i]
	key, ok := m.cache.Lookup(v.cache)
	if !ok {
		e := &m.cache.Entries[v.cache]
		if e.Pending == 0 {
			e.Pending = m.token()
			e.RequestedMarker = e.Marker
			m.emit(Action{Kind: Derive, Token: e.Pending, Private: m.material.PrivateRef, Public: e.Public})
		}
		return
	}
	p := precheck(m.cfg.UID, m.material)
	b, _ := p.Marshal()
	h, _ := RecordHeader(2, m.cfg.RecordSID, 104+len(b)+16)
	h = append(h, m.ivs[v.cache][:]...)
	h = append(h, m.material.Public[:]...)
	m.seal(v.link, 2, p.Opcode, AESGCM, key, h, b)
	v.stage = precheckPending
	v.last = m.now
}
func (m *Machine) derived(e Event) {
	if m.state != Connecting {
		return
	}
	for i := 0; i < m.cache.Count; i++ {
		v := &m.cache.Entries[i]
		if v.Pending == e.Token && e.Token != 0 {
			v.Pending = 0
			if e.Key == 0 || e.IV == [12]byte{} {
				m.close("key derivation failed", false)
				return
			}
			v.Key = e.Key
			v.CachedMarker = v.RequestedMarker
			m.ivs[i] = e.IV
			for j := 0; j < m.count; j++ {
				s := m.endpoints[j]
				if !s.master && s.cache == i && s.stage < precheckReady && s.connected && (s.link.Transport == TCP || s.stage == helloObserved) {
					m.prepare(j)
				}
			}
			return
		}
	}
}
func (m *Machine) control(i int, op uint16) {
	v := &m.endpoints[i]
	key, ok := m.cache.Lookup(v.cache)
	if !ok {
		return
	}
	// Selected UDP/TCP info uses flag 1; eligible unselected UDP servers use flag 0.
	p := rendezvous(op, m.cfg.UID, m.material, m.cfg.LocalNAT, v.relayID, v.link == m.relaySelected)
	b, _ := p.Marshal()
	h, _ := RecordHeader(4, m.cfg.RecordSID, 4+len(b)+16)
	h = append(h, 0, 0, 0, 0)
	put32(h[12:], v.sid)
	m.seal(v.link, 4, op, AESGCM, key, h, b)
	if op == 0x0240 {
		v.lastDirect = m.now
	} else {
		v.last = m.now
	}
}

// Independent random serial origins and attempt-long history are local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#receive-catalogue
func (m *Machine) hello(i int) {
	v := &m.endpoints[i]
	// Retain every issued serial until endpoint/attempt retirement. Retrying
	// must not revoke a response still in flight. The fixed cap covers the entire
	// local attempt deadline.
	if v.hello != 0 {
		if v.helloCount == len(v.helloPrior) {
			return
		}
		v.helloPrior[v.helloCount] = v.hello
		v.helloCount++
	}
	// Zero is only our unissued sentinel. Skip it on
	// wrap; the bounded attempt cannot exhaust the remaining 32-bit space.
	m.helloSerial++
	if m.helloSerial == 0 {
		m.helloSerial++
	}
	v.hello = m.helloSerial
	p := controlPacket(0x8003, 0x3f, 8)
	put32(p.Body, v.hello)
	put16(p.Body[4:], uint16(m.cfg.RecordSID))
	p.Body[6] = 1
	m.legacy(v.link, p)
	v.last = m.now
}
func (v *endpoint) ownsHello(serial uint32) bool {
	if serial == 0 {
		return false
	}
	if serial == v.hello {
		return true
	}
	for _, prior := range v.helloPrior[:v.helloCount] {
		if serial == prior {
			return true
		}
	}
	return false
}

func (m *Machine) legacy(l Link, p Packet) {
	b, e := EncodeLegacy(p, m.key)
	if e == nil {
		m.emit(Action{Kind: Send, Link: l, Data: b})
	}
}
func (m *Machine) wire(e Event) {
	if len(e.Data) >= 4 && le32(e.Data) == 0xcc51 {
		m.openRecord(e)
		return
	}
	p, err := DecodeLegacy(e.Data, m.key)
	if err != nil {
		return
	}
	m.peer(e.Link, p)
}

// openRecord binds authentication to the admitted endpoint generation.
// Authenticate the record with its exact outer context before interpreting controls:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#receive-processing
func (m *Machine) openRecord(e Event) {
	r, err := ParseRecord(e.Data)
	if err != nil || r.SID != m.cfg.RecordSID {
		return
	}
	i := m.find(e.Link)
	if i < 0 {
		return
	}
	v := m.endpoints[i]
	if v.stage == endpointTerminal {
		return
	}
	if r.Unknown() || r.Type == 2 {
		m.receiveOddity(UnknownRecord, 0, r.Type)
		return // Unknown types have no authenticated-body or liveness semantics.
	}
	if m.state == Established && (r.Type != 4 || !(m.direct && v.stage == punchRecovery || !m.direct && v.link == m.path)) {
		return
	}
	key, prefix, ok := m.recordContext(v, r)
	if !ok {
		return
	}
	if len(e.Data) < prefix+16 || m.pendingFor(e.Link, Open, 0) {
		return
	}
	p := operation{token: m.token(), kind: Open, link: e.Link, typ: r.Type, size: len(e.Data) - prefix - 16, marker: e.Marker}
	if prefix == 16 {
		p.sid = le32(r.Body)
	}
	if !m.reserve(p) {
		return
	}
	m.emit(Action{Kind: Open, Token: p.token, Link: e.Link, Mode: AESGCM, Key: key, AAD: append([]byte(nil), e.Data[:prefix]...), Data: append([]byte(nil), e.Data[prefix:]...)})
}

// recordContext gives each endpoint stage an explicit authenticated ingress policy.
func (m *Machine) recordContext(v endpoint, r Record) (key KeyRef, prefix int, ok bool) {
	prefix = 12
	if r.Type == 1 {
		if !v.master || v.stage != endpointInitial || m.state != Connecting {
			return 0, 0, false
		}
		key = m.material.MasterRef
	} else {
		if v.master {
			return 0, 0, false
		}
		key, ok = m.cache.Lookup(v.cache)
		if !ok {
			return 0, 0, false
		}
		switch r.Type {
		case 3:
			if v.stage != precheckPending {
				return 0, 0, false
			}
		case 4:
			if v.stage < precheckReady || len(r.Body) < 4 {
				return 0, 0, false
			}
			prefix = 16
		case 14:
			if v.stage < precheckPending {
				return 0, 0, false
			}
		default:
			return 0, 0, false
		}
	}
	return key, prefix, true
}

func (m *Machine) cryptoDone(e Event) {
	var p operation
	found := false
	for i, v := range m.pending {
		if v.token == e.Token && e.Token != 0 {
			if e.Kind == Encrypted && v.kind != Seal || e.Kind == Decrypted && v.kind != Open {
				return
			}
			p = v
			m.pending[i] = operation{}
			found = true
			break
		}
	}
	if !found {
		return
	}
	if e.Kind == Encrypted {
		want := p.size + 16
		if p.typ == 1 {
			want = 256
		}
		if len(e.Data) != want {
			m.close("invalid crypto completion", false)
			return
		}
		b := append([]byte(nil), p.aad[:p.aadN]...)
		b = append(b, e.Data...)
		m.relaySend(p)
		m.emit(Action{Kind: Send, Link: p.link, Data: b})
		return
	}
	if len(e.Data) != p.size {
		return
	}
	m.decrypted(p, e.Data)
}

// Unknown controls do not confer authority; known layouts and checks:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#received-inner-messages
func (m *Machine) decrypted(p operation, data []byte) {
	i := m.find(p.link)
	if i < 0 {
		return
	}
	v := &m.endpoints[i]
	if p.typ == 4 && p.sid != v.sid {
		return
	}
	if p.typ == 14 {
		a, err := ParseAlert(data)
		if err != nil || a.Nonce != m.material.P2PNonce || v.sid != 0 && a.ServerSID != v.sid {
			return
		}
		if a.Trigger != 2 && a.Trigger != 4 {
			m.receiveOddity(AlertTrigger, 0, a.Trigger)
		}
		if a.Code == 1 || a.Code == 2 {
			v.sid = a.ServerSID
			reason := DeviceOffline
			if a.Code == 2 {
				reason = DeviceAsleep
			}
			m.terminalEndpoint(i, reason)
		} else {
			m.receiveOddity(UnknownAlert, 0, 14)
		}
		return
	}
	packet, err := ParsePacket(data)
	if err != nil {
		return
	}
	if p.typ == 1 && packet.Opcode != 0x100c {
		m.receiveOddity(UnknownControl, packet.Opcode, p.typ)
		return
	}
	c, err := ParseControl(packet)
	if err != nil {
		return
	}
	if c.Unknown {
		m.receiveOddity(UnknownControl, packet.Opcode, p.typ)
		return
	}
	if c.UID != m.cfg.UID {
		return
	}
	switch packet.Opcode {
	case 0x100c:
		if p.typ != 1 || c.Nonce != m.material.MasterNonce || v.stage != endpointInitial || m.state != Connecting {
			return
		}
		m.layoutOddity(packet.Opcode, c.layoutOddity)
		if c.unknownUnit {
			m.receiveOddity(UnknownMasterUnit, packet.Opcode, p.typ)
		}
		m.masterResult(i, c, p.marker)
	case 0x0218:
		m.precheckResult(i, p, c)
	case 0x030d:
		m.punchResult(i, p, c)
	case 0x020d:
		m.relayResult(i, p, c)
	case 0x030e:
		m.relayInfo(i, p, c)
	}
}

// Nonce, UID and server-session handoff:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/master-record.md#handshake-replies-record-type-3
func (m *Machine) precheckResult(i int, p operation, c Control) {
	v := &m.endpoints[i]
	if p.typ != 3 || v.stage != precheckPending || c.Nonce != m.material.P2PNonce || c.ServerSID == 0 || m.state != Connecting {
		return
	}
	v.sid = c.ServerSID
	v.stage = precheckReady
	v.peerNAT = c.PeerNAT
	if v.link.Transport == UDP {
		m.udpResponse = true
		m.selectTransport(UDP)
	}
	// Selection may have happened while this server's authentication was pending.
	// Its transport owns the late completion regardless of another server's fate.
	if m.selection && m.selected == v.link.Transport && v.stage == precheckReady {
		m.startRendezvous(i)
	}
}

// R13 requires agreement between the outer context and the session found by RID:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#receive-processing
func (m *Machine) punchResult(i int, p operation, c Control) {
	if p.typ != 4 || c.Nonce != m.material.P2PNonce || c.RID != m.material.RID || !m.selection || m.selected != UDP {
		return
	}
	if m.state == Established {
		if m.direct {
			m.legacy(m.path, knock(0x0403, m.cfg.UID, m.material.RID))
		}
		return
	}
	if m.endpoints[i].directExpired {
		return
	}
	until := m.endpoints[i].directUntil
	for _, a := range c.WAN[:c.WANCount] {
		m.addCandidate(a, until)
	}
	for _, a := range c.LAN {
		m.addCandidate(a, until)
	}
	m.knocks()
}

// Pending, ready, refusal and unknown results retain distinct meanings:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/relay.md#relay-result-codes
func (m *Machine) relayResult(i int, p operation, c Control) {
	v := &m.endpoints[i]
	if p.typ != 4 || c.RID != m.material.RID || v.stage < rendezvousPending || v.stage > relayRefused || m.state != Connecting {
		return
	}
	if c.Result != 0 && c.Result != -7 && relayFailure(c.Result) == 0 {
		m.receiveOddity(UnknownRelayResult, 0x020d, p.typ)
		return
	}
	m.observeRelayResult(v, c.Result)
	if v.stage != rendezvousPending {
		return
	}
	if c.Result == 0 {
		if v.link.Transport == TCP && m.relaySelected.Address.IsValid() && m.relaySelected != v.link {
			return
		}
		if !m.relaySelected.Address.IsValid() {
			m.relaySelected = v.link
		}
		v.stage = relayInfoPending
		m.relayPhase(RelayAccepted)
		m.control(i, 0x0241)
	} else if reason := relayFailure(c.Result); reason != 0 {
		m.refuseRelay(i, reason)
	}
}

// A ready result alone is insufficient; session info supplies the relay allocation:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/relay.md#record-relay-messages-client-sdk
func (m *Machine) relayInfo(i int, p operation, c Control) {
	v := &m.endpoints[i]
	if p.typ != 4 || c.RID != m.material.RID || !m.selection || v.link.Transport != m.selected || v.stage != relayInfoPending && v.stage != relayInfoReady {
		return
	}
	if m.state == Established && (m.direct || v.link != m.path) {
		return
	}
	increment(&m.progress.InfoReceived)
	m.relayPhase(RelayInfoReceived)
	v.relayID = uint16(c.RelayID)
	v.relayReady = true
	if m.state == Established {
		m.relay = v.relayID
	}
	v.stage = relayInfoReady
	m.control(i, 0x030f)
	if m.state == Connecting {
		m.relayPending = v.link
	}
}

func (m *Machine) masterResult(i int, c Control, at Marker) {
	v := &m.endpoints[i]
	if len(c.Servers) == 0 {
		return
	}
	v.stage = masterAnswered
	if v.link.Transport == UDP {
		m.udpResponse = true
	}
	for _, s := range c.Servers {
		idx, err := m.cache.Install(s, at)
		if err != nil {
			continue
		}
		if v.link.Transport == UDP {
			l := Link{Address: s.Address}
			if m.find(l) >= 0 {
				continue
			}
			j := m.add(endpoint{link: l, connected: true, cache: idx})
			if j >= 0 {
				m.hello(j)
			}
		} else {
			exists := false
			for j := 0; j < m.count; j++ {
				old := m.endpoints[j]
				if !old.master && old.link.Transport == TCP && old.link.Address.Addr() == s.Address.Addr() {
					exists = true
				}
			}
			if !exists {
				m.openTCP(s.Address.Addr(), false, idx)
			}
		}
	}
}
func (m *Machine) selectTransport(t Transport) {
	if m.selection {
		return
	}
	m.selection = true
	m.selected = t
	for i := 0; i < m.count; i++ {
		v := &m.endpoints[i]
		if v.link.Transport != t || v.master {
			m.retireEndpoint(i)
		} else if v.stage == precheckReady {
			m.startRendezvous(i)
		}
	}
}

// Parallel direct and relay work retain independent lifetimes:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#udp-establishment
func (m *Machine) startRendezvous(i int) {
	v := &m.endpoints[i]
	v.stage = rendezvousPending
	// Scheduler arguments belong to this task.
	v.directUntil = after(m.start, 60*time.Second)
	v.directInterval = 200 * time.Millisecond
	if int(m.cfg.LocalNAT)+int(v.peerNAT) == 6 {
		v.directUntil = after(m.now, 400*time.Millisecond)
	} else if m.cfg.P2PPortCount >= 4 {
		v.directInterval = 50 * time.Millisecond
	}
	if v.link.Transport == UDP && v.peerNAT != 10 {
		m.control(i, 0x0240)
	}
	m.control(i, 0x020c)
}

// chooseTCP uses elapsed-time equivalents of the 1500/5000-ms poll gates.
// A UDP master/precheck response extends the preference window.
// Monotonic selection approximates native accumulated-counter gates:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#udptcp-race
func (m *Machine) chooseTCP() {
	if m.selection || m.state != Connecting {
		return
	}
	gate := 1500 * time.Millisecond
	if m.udpResponse {
		gate = 5 * time.Second
	}
	if m.now.Sub(m.start) < gate {
		return
	}
	for i := 0; i < m.count; i++ {
		v := m.endpoints[i]
		if v.link.Transport == TCP && !v.master && v.stage == precheckReady {
			m.selectTransport(TCP)
			return
		}
	}
}

// addCandidate keeps a bounded union across servers. Retired addresses still
// occupy their slot so a delayed authenticated punch cannot reauthorize them.
func (m *Machine) addCandidate(a netip.AddrPort, until core.Instant) {
	if !a.IsValid() {
		return
	}
	for i, old := range m.candidates[:m.candidateCount] {
		if old == a {
			// A shared address remains eligible while any authorizing task lives.
			if m.candidateUntil[i] != 0 && (until == 0 || until > m.candidateUntil[i]) {
				m.candidateUntil[i] = until
			}
			return
		}
	}
	if m.candidateCount == len(m.candidates) {
		return
	}
	m.candidates[m.candidateCount] = a
	m.candidateUntil[m.candidateCount] = until
	m.candidateCount++
}

func (m *Machine) hasLiveCandidates() bool {
	for i := 0; i < m.candidateCount; i++ {
		if !m.candidateFailed[i] {
			return true
		}
	}
	return false
}

func (m *Machine) knocks() {
	for i, a := range m.candidates[:m.candidateCount] {
		if m.candidateFailed[i] {
			continue
		}
		m.legacy(Link{Address: a}, knock(0x0403, m.cfg.UID, m.material.RID))
	}
	m.lastKnock = m.now
}
func (m *Machine) peer(l Link, p Packet) {
	if p.Opcode == 0x8004 || p.Opcode == 0x8005 {
		m.observeHello(l, p)
		return
	}
	if p.Opcode >= 0x0401 && p.Opcode <= 0x0406 {
		m.peerKnock(l, p)
		return
	}
	if m.state != Established || l != m.path {
		return
	}
	dataOp, aliveOp, closeOp := uint16(0x0408), uint16(0x0428), uint16(0x0418)
	if !m.direct {
		dataOp, aliveOp, closeOp = 0x0503, 0x0523, 0x0513
		if p.ID != m.relay {
			return
		}
	}
	// Route values are opaque. Authority comes from the retained path,
	// opcode direction, allocation (relay only), RID and secure datatype.
	switch p.Opcode {
	case dataOp:
		rid, b, err := ParseData(p)
		if err != nil || rid != m.material.RID || p.Channel != 0 || p.Type&3 != 1 {
			return
		}
		m.routeOddity(p)
		m.alive = 0
		m.emit(Action{Kind: Received, Data: append([]byte(nil), b...)})
	case aliveOp, closeOp:
		// Only relay alive permits a tail after the extended RID prefix.
		// Direct alive and both CLOSE forms retain their fixed layouts.
		if len(p.Body) < 8 || (m.direct || p.Opcode == closeOp) && len(p.Body) != 8 || [8]byte(p.Body) != m.material.RID || p.Flags&8 == 0 {
			return
		}
		m.routeOddity(p)
		if p.Opcode == closeOp {
			m.close("remote close", false)
		} else {
			m.layoutOddity(p.Opcode, len(p.Body) != 8)
			m.alive = 0
		}
	}
}

// observeHello separates NAT observation from handshake progression. The
// alternate-address policy is conservative/inferred: an extended 8005 must
// echo an outstanding serial/instance and come from a master-advertised IPv4.
// Short replies have no echoed identity and cannot establish attempt ownership.
// Legacy message types and NAT classification determine observation handling.
// Reject short uncorrelated replies; advertised-source admission is local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#nat-classification
func (m *Machine) observeHello(l Link, p Packet) {
	if m.state != Connecting || l.Transport != UDP || len(p.Body) < 24 {
		return
	}
	i := m.find(l)
	if p.Opcode == 0x8005 {
		known := false
		for _, e := range m.cache.Entries[:m.cache.Count] {
			known = known || e.Address == l.Address.Addr()
		}
		if !known {
			return
		}
		i = -1
		for j, v := range m.endpoints[:m.count] {
			if !v.master && v.link.Transport == UDP && v.stage < endpointTerminal && v.ownsHello(le32(p.Body[16:])) {
				if i >= 0 {
					return
				} // Ambiguous serials cannot authorize another source.
				i = j
			}
		}
	}
	if i < 0 {
		return
	}
	v := &m.endpoints[i]
	if v.master || v.hello == 0 || v.stage >= endpointTerminal {
		return
	}
	if !v.ownsHello(le32(p.Body[16:])) || le16(p.Body[20:]) != uint16(m.cfg.RecordSID) {
		return
	}
	observed, err := ParseAddress(p.Body[:16])
	if err != nil {
		return
	}
	m.layoutOddity(p.Opcode, len(p.Body) != 24)
	m.nat.Observe(v.link.Address, observed, p.Opcode == 0x8005)
	if nat := m.nat.Type(); nat != 0 {
		m.cfg.LocalNAT = nat
	}
	if v.stage == endpointInitial {
		v.stage = helloObserved
		m.prepare(i)
	}
}

// Direct establishment requires the peer exchange, not only a server punch:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#receive-processing
// Knock layouts:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/framing.md#generated-control-packets
func (m *Machine) peerKnock(l Link, p Packet) {
	if l.Transport != UDP || !m.selection || m.selected != UDP || m.state == Established && (!m.direct || l != m.path) {
		return
	}
	candidate := -1
	for i, a := range m.candidates[:m.candidateCount] {
		if l.Address == a && !m.candidateFailed[i] {
			candidate = i
			break
		}
	}
	if candidate < 0 {
		return
	}
	c, err := ParseControl(p)
	if err != nil || c.UID != m.cfg.UID || c.RID != m.material.RID || p.Route&0x200 != 0 {
		return
	}
	m.layoutOddity(p.Opcode, c.layoutOddity)
	if p.Route != 0x33 {
		m.receiveOddity(OpaqueRoute, p.Opcode, 0)
	}
	switch p.Opcode {
	case 0x0401:
		m.knockReply[candidate] = true
		m.legacy(l, knock(0x0402, m.cfg.UID, m.material.RID))
	case 0x0402, 0x0405:
		if p.Opcode == 0x0405 && byte(c.Result) == 0 {
			return
		}
		q := knock(0x0404, m.cfg.UID, m.material.RID)
		q.Sequence = p.Sequence
		m.legacy(l, q)
		if m.state == Connecting {
			m.establish(l, true)
		}
	case 0x0404, 0x0406:
		if !m.knockReply[candidate] || p.Opcode == 0x0406 && c.Result == 0 {
			return
		}
		if m.state == Connecting {
			m.establish(l, true)
		}
	}
}

// establish retires discovery and selection work. Direct UDP retains only
// authenticated punch recovery; relay retains only its own info/ACK recovery.
// Selection and cleanup retain only the recovery work owned by the active path.
// Keep the selected route; this does not implement same-RID migration:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/session.md#path-migration
func (m *Machine) establish(l Link, direct bool) {
	m.resolveToken, m.materialToken = 0, 0
	for i := range m.cache.Entries {
		m.cache.Entries[i].Pending = 0
	}
	for i, p := range m.pending {
		if direct || p.link != l || p.kind != Seal || p.opcode != 0x030f {
			m.pending[i] = operation{}
		}
	}
	m.state = Established
	m.path = l
	m.direct = direct
	if !direct {
		m.progress.Selected = true
		m.relayPhase(RelaySelected)
	}
	m.relayPending = Link{}
	m.alive = 0
	m.aliveAt = after(m.now, time.Second)
	for i := 0; i < m.count; i++ {
		v := &m.endpoints[i]
		if v.stage == endpointRetired || v.link == l {
			continue
		}
		if direct && v.directPending() {
			v.stage = punchRecovery
		} else {
			m.retireEndpoint(i)
		}
	}
	m.emit(Action{Kind: Ready, Link: l, Progress: m.RelayProgress()})
}
func (m *Machine) sessionPacket(op uint16, body []byte) Packet {
	route := uint16(0x21)
	id := le16(m.material.RID[:])
	if !m.direct {
		route = 0x24
		id = m.relay
	}
	return Packet{Revision: 29, Flags: 9, Opcode: op, Route: route, ID: id, Body: body}
}
func (m *Machine) sendData(b []byte) {
	body := make([]byte, 12+len(b))
	put32(body, 12)
	copy(body[4:], m.material.RID[:])
	copy(body[12:], b)
	op := uint16(0x0407)
	if !m.direct {
		op = 0x0504
	}
	p := m.sessionPacket(op, body)
	p.Type = 1
	p.Sequence = m.sequence
	m.sequence++
	m.legacy(m.path, p)
}

// tick applies bounded catch-up for alive counters and at most one retry per
// endpoint. The configured retry intervals are:
// UDP query/hello/precheck 500 ms, TCP precheck 1 s, TCP relay retry 2 s,
// direct knocks 200 ms. The total attempt budget is enforced at Step ingress.
func (m *Machine) tick() {
	if m.state == Established {
		m.tickAlive()
		return
	}
	if m.selectReadyRelay() {
		return
	}
	m.chooseTCP()
	m.next = after(m.now, 10*time.Millisecond)
	for i := 0; i < m.count; i++ {
		m.retryEndpoint(i)
	}
	if m.selection && m.selected == UDP && m.now.Sub(m.lastKnock) >= 200*time.Millisecond {
		m.knocks()
	}
}

// tickAlive handles only the selected session's liveness budget.
// Native liveness is tick-counted, with suppressed early alive sends:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/session.md#session-status-and-liveness
func (m *Machine) tickAlive() {
	if m.now < m.aliveAt {
		return
	}
	ticks := int64(m.now.Sub(m.aliveAt)/time.Second) + 1
	if ticks > int64(31-m.alive) {
		m.close("liveness timeout", false)
		return
	}
	m.alive += int(ticks)
	m.aliveAt = after(m.aliveAt, time.Duration(ticks)*time.Second)
	if m.alive >= 5 || m.alive%2 == 0 {
		op := uint16(0x0427)
		if !m.direct {
			op = 0x0524
		}
		m.legacy(m.path, m.sessionPacket(op, append([]byte(nil), m.material.RID[:]...)))
	}
}

// expireDirectTasks applies the independent task lifetime before completions as
// well as ticks. Sum-six always uses 400 ms, regardless of the optional port count. Other
// parallel tasks remain bounded by the overall 60-second attempt deadline.
func (m *Machine) expireDirectTasks() {
	if m.state != Connecting {
		return
	}
	// A punch response does not extend the direct task lifetime. Retire its
	// discovered addresses too, unless another live task authorizes them.
	for i, until := range m.candidateUntil[:m.candidateCount] {
		if until != 0 && m.now >= until {
			m.candidateFailed[i] = true
			m.knockReply[i] = false
		}
	}
	for i := 0; i < m.count; i++ {
		v := &m.endpoints[i]
		if !v.directPending() || m.now < v.directUntil {
			continue
		}
		v.directExpired = true
		// Pending encryptions no longer authorize a direct request on this task.
		for j, p := range m.pending {
			if p.link == v.link && p.kind == Seal && p.opcode == 0x0240 {
				m.pending[j] = operation{}
			}
		}
		if v.stage == relayRefused {
			m.terminalEndpoint(i, v.failure|ConnectionTimeout)
		}
	}
}

// retryEndpoint schedules direct work independently of discovery/relay stage.
// Relay acceptance or traffic cannot postpone an owned direct negotiation.
// Retry spacing follows the selected transport within our total attempt deadline:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/connect.md#udptcp-race
func (m *Machine) retryEndpoint(i int) {
	v := &m.endpoints[i]
	if v.stage >= endpointTerminal || !v.connected {
		return
	}
	if v.directPending() && m.now.Sub(v.lastDirect) >= v.directInterval {
		m.control(i, 0x0240)
	}
	interval := 500 * time.Millisecond
	if v.link.Transport == TCP {
		interval = time.Second
		if v.stage >= rendezvousPending {
			interval = 2 * time.Second
		}
	}
	if m.now.Sub(v.last) < interval {
		return
	}
	if v.master {
		if v.stage == endpointInitial {
			m.query(i)
		}
		return
	}
	switch v.stage {
	case endpointInitial:
		if v.link.Transport == UDP {
			m.hello(i)
		} else {
			m.prepare(i)
		}
	case helloObserved, precheckPending:
		m.prepare(i)
	case rendezvousPending:
		m.control(i, 0x020c)
	case relayInfoPending:
		m.control(i, 0x0241)
	}
}

// selectReadyRelay revalidates the preference and then scans the bounded set
// of authenticated allocations. A failed preference never strands a competitor.
func (m *Machine) selectReadyRelay() bool {
	l := m.relayPending
	m.relayPending = Link{}
	if m.tryRelay(l) {
		return true
	}
	for _, v := range m.endpoints[:m.count] {
		if v.relayReady && m.tryRelay(v.link) {
			return true
		}
	}
	return false
}

func (m *Machine) tryRelay(l Link) bool {
	if i := m.find(l); i >= 0 && m.endpoints[i].connected && m.endpoints[i].stage == relayInfoReady && m.selection && m.selected == l.Transport {
		m.relay = m.endpoints[i].relayID
		m.establish(l, false)
		return true
	}
	return false
}

// Repeated close packets require no acknowledgment before local disposal:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/session.md#close
func (m *Machine) close(reason string, send bool) {
	if m.state == Closed {
		return
	}
	if send && m.state == Established {
		op, n := uint16(0x0417), 3
		if !m.direct {
			op, n = 0x0514, 5
		}
		for i := 0; i < n; i++ {
			m.legacy(m.path, m.sessionPacket(op, append([]byte(nil), m.material.RID[:]...)))
		}
	}
	m.emit(Action{Kind: CancelTimer})
	m.emit(Action{Kind: Disposed})
	m.progress = m.RelayProgress() // Snapshot outstanding paths before cleanup.
	m.emit(Action{Kind: Failed, Reason: reason, Progress: m.progress})
	m.state = Closed
	m.resolveToken, m.materialToken = 0, 0
	m.material = Material{}
	m.cache.Clear()
	m.ivs = [12][12]byte{}
	m.pending = [64]operation{}
	m.endpoints = [32]endpoint{}
	m.count = 0
	m.cfg.License = License{}
	m.cfg.UID = [20]byte{}
	m.key = [16]byte{}
	m.candidates = [12]netip.AddrPort{}
	m.candidateCount = 0
}
