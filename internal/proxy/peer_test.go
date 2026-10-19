package proxy

// Specification-derived peers adapted from iotc/simulator{,_peer}_test.go and
// the paired RDT/tunnel tests. The shipped RSA private keys are unavailable:
// license validation and master RSA use explicit test seams, independently
// covered by edge crypto fixtures. Rendezvous GCM, DTLS, RDT and tunnel are real.
import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"github.com/go-json-experiment/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	pion "github.com/pion/dtls/v3"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/dtls"
	"github.com/zmerlynn/seestar-wormhole/internal/edge/iotcedge"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
)

type peerPackets struct {
	ctx    context.Context
	rx, tx chan []byte
}

func (p *peerPackets) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case d := <-p.rx:
		return copy(b, d), streamAddr{}, nil
	case <-p.ctx.Done():
		return 0, nil, net.ErrClosed
	}
}
func (p *peerPackets) WriteTo(b []byte, _ net.Addr) (int, error) {
	select {
	case p.tx <- append([]byte(nil), b...):
		return len(b), nil
	case <-p.ctx.Done():
		return 0, net.ErrClosed
	}
}
func (p *peerPackets) Close() error                     { return nil }
func (p *peerPackets) LocalAddr() net.Addr              { return streamAddr{} }
func (p *peerPackets) SetDeadline(time.Time) error      { return nil }
func (p *peerPackets) SetReadDeadline(time.Time) error  { return nil }
func (p *peerPackets) SetWriteDeadline(time.Time) error { return nil }

type simulatedWire struct {
	capture                    func(client bool, datagram []byte)
	controlReply               func(channel byte, line []byte) []byte           // Scripted control replies/events, owned by peer loop.
	tunnelInput                func(p2ptunnel.Event, *p2ptunnel.Machine)        // Test-only observation before each peer tunnel step.
	socketScript               func(p2ptunnel.Action) ([]p2ptunnel.Event, bool) // Scripted local sockets, owned by peer loop.
	rdtPrefix                  []byte                                           // One literal authenticated test packet before normal RDT output.
	sambaAddress               string
	outage                     func() bool
	transport                  func(context.Context, []byte) ([]byte, error)
	suite                      uint16
	responseBytes              int
	rdtGapAfter, rdtGapPackets int    // Synthetic missing DATA followed by a device-window burst.
	httpResponse               []byte // Standard HTTP fixture on port 80.
	bulkBurst                  bool   // Native-size RDT backlog and immediate socket reads for capacity tests.
	verify                     bool
	delay                      time.Duration
	impairEvery                int
	packetIndex                int
	held                       []byte

	t        *testing.T
	fixtures map[uint16][]byte
	crypt    iotcedge.IOTCCrypto
	mat      iotc.Material
	sid      uint32
	pair     cipher.AEAD
	iv       [12]byte
	device   iotc.Link
	drop     bool
	trace    *[]string
}

func newSimulatedWire(t *testing.T, trace *[]string) *simulatedWire {
	b, err := os.ReadFile("../iotc/testdata/modern-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fs []struct{ Hex string }
	if err = json.Unmarshal(b, &fs, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	p := &simulatedWire{t: t, fixtures: make(map[uint16][]byte), trace: trace, drop: true}
	for _, f := range fs {
		b, err := hex.DecodeString(f.Hex)
		if err != nil {
			t.Fatal(err)
		}
		p.fixtures[binary.LittleEndian.Uint16(b[8:])] = b
	}
	return p
}
func (p *simulatedWire) Run(ctx context.Context, actions <-chan iotc.Action, emit func(iotc.Event) bool) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	downstream := emit
	emit = func(e iotc.Event) bool {
		if e.Kind == iotc.Wire && p.outage != nil && p.outage() {
			return true
		}
		return downstream(e)
	}
	packets := &peerPackets{ctx, make(chan []byte, 64), make(chan []byte, 64)}
	var wg sync.WaitGroup
	wg.Go(func() { p.devicePeer(ctx, packets) })
	defer func() { cancel(); wg.Wait(); p.crypt.Clear() }()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-packets.tx:
			if p.capture != nil {
				p.capture(false, b)
			}
			body := make([]byte, 12+len(b))
			binary.LittleEndian.PutUint32(body, 12)
			copy(body[4:], p.mat.RID[:])
			copy(body[12:], b)
			q := iotc.Packet{Revision: 29, Flags: 9, Opcode: 0x0408, Route: 0x12, Type: 1, Body: body}
			p.legacy(p.device, q, emit)
		case a := <-actions:
			e := iotc.Event{Attempt: a.Attempt, Token: a.Token, Link: a.Link}
			switch a.Kind {
			case iotc.Generate, iotc.Derive, iotc.Seal, iotc.Open:
				var err error
				if a.Kind == iotc.Seal && a.Mode == iotc.RawMasterRSA {
					e.Kind = iotc.Encrypted
					e.Data = append([]byte(nil), a.Data...)
				} else {
					e, err = p.crypt.Execute(a)
				}
				if err != nil {
					p.t.Error("crypto effect", err)
					return
				}
				if a.Kind == iotc.Generate {
					p.mat = e.Material
				}
				if !emit(e) {
					return
				}
			case iotc.Resolve:
				e.Kind = iotc.Resolved
				e.Addresses = []netip.Addr{netip.MustParseAddr("192.0.2.100")}
				emit(e)
			case iotc.OpenTCP:
				e.Kind = iotc.LinkFailed
				emit(e)
			case iotc.Send:
				if p.outage != nil && p.outage() {
					e.Kind = iotc.Sent
					if !emit(e) {
						return
					}
					continue
				}
				if p.transport != nil {
					var err error
					a.Data, err = p.transport(ctx, a.Data)
					if err != nil {
						if ctx.Err() == nil {
							p.t.Error(err)
						}
						return
					}
				}
				p.send(a, packets, emit)
				e.Kind = iotc.Sent
				if !emit(e) {
					return
				}
			case iotc.Disposed:
				return
			}
		}
	}
}
func (p *simulatedWire) legacy(l iotc.Link, q iotc.Packet, emit func(iotc.Event) bool) {
	b, err := iotc.EncodeLegacy(q, iotc.ScrambleKey([16]byte{}))
	if err != nil {
		p.t.Error(err)
		return
	}
	emit(iotc.Event{Kind: iotc.Wire, Link: l, Data: b, Marker: iotc.Marker{Seconds: 100}})
}
func (p *simulatedWire) encrypted(l iotc.Link, typ byte, plain []byte, emit func(iotc.Event) bool) {
	a, iv := p.pair, p.iv
	prefix := 0
	if typ == 1 {
		b, _ := aes.NewCipher(p.mat.MasterKey[:])
		a, _ = cipher.NewGCM(b)
		iv = p.mat.MasterIV
	}
	if typ == 4 {
		prefix = 4
	}
	aad, _ := iotc.RecordHeader(typ, p.sid, prefix+len(plain)+16)
	if typ == 4 {
		aad = append(aad, 0x55, 0, 0, 0)
	}
	b := a.Seal(nil, iv[:], plain, aad)
	emit(iotc.Event{Kind: iotc.Wire, Link: l, Data: append(aad, b...), Marker: iotc.Marker{Seconds: 100}})
}
func (p *simulatedWire) send(a iotc.Action, packets *peerPackets, emit func(iotc.Event) bool) {
	b := a.Data
	if len(b) >= 12 && binary.LittleEndian.Uint32(b) == 0xcc51 {
		r, err := iotc.ParseRecord(b)
		if err != nil {
			p.t.Error(err)
			return
		}
		p.sid = r.SID
		if r.Type == 1 {
			// The existing master fixture's public key is P-256 scalar 1.
			plain := append([]byte(nil), p.fixtures[0x100c]...)
			binary.LittleEndian.PutUint16(plain[16:], p.mat.MasterNonce)
			p.encrypted(a.Link, 1, plain, emit)
			return
		}
		prefix := 16
		if r.Type == 2 {
			v, err := x509.ParsePKIXPublicKey(b[24:115])
			if err != nil {
				p.t.Error(err)
				return
			}
			pub, err := v.(*ecdsa.PublicKey).ECDH()
			if err != nil {
				p.t.Error(err)
				return
			}
			scalar := make([]byte, 32)
			scalar[31] = 1
			priv, _ := ecdh.P256().NewPrivateKey(scalar)
			secret, err := priv.ECDH(pub)
			if err != nil {
				p.t.Error(err)
				return
			}
			block, _ := aes.NewCipher(secret[:16])
			p.pair, _ = cipher.NewGCM(block)
			copy(p.iv[:], b[12:24])
			prefix = 116
		}
		plain, err := p.pair.Open(nil, p.iv[:], b[prefix:], b[:prefix])
		if err != nil {
			p.t.Error(err)
			return
		}
		q, err := iotc.ParsePacket(plain)
		if err != nil {
			p.t.Error(err)
			return
		}
		switch q.Opcode {
		case 0x0217:
			reply := iotc.Packet{Revision: 29, Opcode: 0x0218, Route: 0x24, Body: make([]byte, 56)}
			binary.LittleEndian.PutUint16(reply.Body, p.mat.P2PNonce)
			binary.LittleEndian.PutUint32(reply.Body[4:], 0x55)
			copy(reply.Body[8:], q.Body[4:24])
			binary.LittleEndian.PutUint32(reply.Body[44:], 3)
			raw, _ := reply.Marshal()
			p.encrypted(a.Link, 3, raw, emit)
		case 0x0240:
			raw := append([]byte(nil), p.fixtures[0x030d]...)
			binary.LittleEndian.PutUint16(raw[16:], p.mat.P2PNonce)
			copy(raw[20:40], q.Body[:20])
			copy(raw[120:128], p.mat.RID[:])
			p.encrypted(a.Link, 4, raw, emit)
		}
		return
	}
	q, err := iotc.DecodeLegacy(b, iotc.ScrambleKey([16]byte{}))
	if err != nil {
		p.t.Error(err)
		return
	}
	switch q.Opcode {
	case 0x8003:
		reply := iotc.Packet{Revision: 29, Opcode: 0x8004, Route: 0x3f, Body: make([]byte, 24)}
		copy(reply.Body, []byte{2, 0, 0x9c, 0x40, 192, 0, 2, 10})
		copy(reply.Body[16:], q.Body[:6])
		p.legacy(a.Link, reply, emit)
	case 0x0403:
		q.Opcode = 0x0405
		q.Route = 0x33
		q.Body[28] = 1
		p.legacy(a.Link, q, emit)
	case 0x0407:
		p.device = a.Link
		_, data, err := iotc.ParseData(q)
		if err != nil {
			p.t.Error(err)
			return
		}
		if p.capture != nil {
			p.capture(true, data)
		}
		// Deterministic packet loss exercises real DTLS handshake retransmission.
		if p.drop {
			p.drop = false
			*p.trace = append(*p.trace, "drop-first-dtls")
			return
		}
		p.packetIndex++
		if len(data) > 1388 {
			p.t.Error("DTLS exceeds path MTU")
			return
		}
		if p.delay > 0 {
			select {
			case <-time.After(p.delay):
			case <-packets.ctx.Done():
				return
			}
		}
		deliver := func(b []byte) {
			select {
			case packets.rx <- append([]byte(nil), b...):
			case <-packets.ctx.Done():
			}
		}
		if p.impairEvery > 0 {
			switch p.packetIndex % p.impairEvery {
			case 0:
				*p.trace = append(*p.trace, "loss")
				return
			case 1:
				*p.trace = append(*p.trace, "duplicate")
				deliver(data)
			case 2:
				*p.trace = append(*p.trace, "reorder")
				p.held = append([]byte(nil), data...)
				return
			}
		}
		deliver(data)
		if p.held != nil {
			deliver(p.held)
			p.held = nil
		}
	case 0x0427:
		q.Opcode = 0x0428
		q.Route = 0x12
		p.legacy(a.Link, q, emit)
	}
}

func (p *simulatedWire) devicePeer(ctx context.Context, packets *peerPackets) {
	_, key, _ := dtls.Credentials([]byte("sim-user"), []byte("sim-pass"))
	opts := []pion.ServerOption{pion.WithCipherSuites(pion.TLS_PSK_WITH_AES_128_GCM_SHA256), pion.WithPSK(func([]byte) ([]byte, error) { return key[:], nil }), pion.WithPSKIdentityHint([]byte{})}
	if p.suite == dtls.CCAC {
		opts = append(opts, pion.WithCipherSuites(pion.TLS_ECDHE_PSK_WITH_CHACHA20_POLY1305_SHA256, pion.TLS_PSK_WITH_AES_128_GCM_SHA256))
	}
	c, err := pion.ServerWithOptions(packets, streamAddr{}, opts...)
	if err != nil {
		p.t.Error(err)
		return
	}
	defer c.Close()
	if err = c.HandshakeContext(ctx); err != nil {
		if ctx.Err() == nil {
			p.t.Error(err)
		}
		return
	}
	inbound := make(chan []byte, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, dtls.MaxApplication)
		for {
			n, err := c.Read(b)
			if err != nil {
				return
			}
			select {
			case inbound <- append([]byte(nil), b[:n]...):
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { c.Close(); <-done }()
	// Large bursts need the same bounded asynchronous send edge as production.
	// Inline c.Write would hold the simulated owner away from ACK/control input.
	outgoing := make(chan []byte, 16)
	completed := make(chan struct{}, 16)
	writeFailed := make(chan error, 1)
	writerStop, writerDone := make(chan struct{}), make(chan struct{})
	if p.bulkBurst {
		go func() {
			defer close(writerDone)
			var held []byte
			var heldSequence rdt.Sequence
			dataPackets, remaining := 0, 0
			for {
				select {
				case <-ctx.Done():
					return
				case <-writerStop:
					return
				case b := <-outgoing:
					// Synthetic physical link: at most 12.8 MB/s of RDT payload,
					// far above the transfer pace. A zero-time burst otherwise
					// measures goroutine scheduling/DTLS drops, not device pacing.
					select {
					case <-time.After(100 * time.Microsecond):
					case <-ctx.Done():
						return
					case <-writerStop:
						return
					}
					packet, _ := rdt.Decode(b)
					skip := false
					if packet.Type == rdt.Data {
						dataPackets++
						if p.rdtGapPackets > 0 && dataPackets == p.rdtGapAfter {
							held, heldSequence, remaining = b, packet.Sequence, p.rdtGapPackets
						}
						if held != nil {
							skip = packet.Sequence == heldSequence
							if !skip {
								remaining--
							}
						}
					}
					if !skip {
						if _, err := c.Write(b); err != nil {
							writeFailed <- err
							return
						}
					}
					if held != nil && remaining == 0 {
						if _, err := c.Write(held); err != nil {
							writeFailed <- err
							return
						}
						held = nil
					}
					select {
					case completed <- struct{}{}:
					case <-ctx.Done():
						return
					case <-writerStop:
						return
					}
				}
			}
		}()
		defer func() { close(writerStop); c.Close(); <-writerDone }()
	}
	rc := rdt.DefaultConfig()
	rc.LocalID = 2
	if p.bulkBurst {
		rc.SendCapacity = 16
		rc.TXBytes = (4 << 20) + p2ptunnel.MaxInput
		rc.TXPackets = 8192
	}
	rm, _ := rdt.New(rc)
	tc := p2ptunnel.DefaultConfig()
	tc.Role = p2ptunnel.Server
	tc.VerifyPorts = p.verify
	tc.AllowedPorts = []uint16{4700, 445, 80}
	if p.socketScript != nil {
		tc.AllowedPorts = append(tc.AllowedPorts, 4350, 4554, 4555)
	}
	tm, _ := p2ptunnel.New(tc)
	var bridge *sambaPeer
	var localEvents <-chan p2ptunnel.Event
	if p.sambaAddress != "" {
		bridge = newSambaPeer(ctx, p.sambaAddress)
		localEvents = bridge.events
		defer bridge.close()
	}
	origin := time.Now()
	timer := time.NewTimer(10 * time.Millisecond)
	var deadlines [2]core.Instant
	defer timer.Stop()
	var queue []layerEvent
	pending := make(map[byte][]byte)
	received := make(map[byte][]byte)
	end := make(map[byte]bool)
	finish := make(map[byte]string)
	httpChannels := make(map[byte]bool)
	enqueue := func(e layerEvent) { queue = append(queue, e) }
	enqueue(layerEvent{r: &rdt.Event{Kind: rdt.Start}})
	for {
		now := core.Instant(time.Since(origin))
		for len(queue) > 0 {
			e := queue[0]
			queue = queue[1:]
			if e.r != nil {
				for _, a := range rm.Step(*e.r, now) {
					switch a.Kind {
					case rdt.ArmTimer:
						deadlines[0] = a.At
					case rdt.CancelTimer:
						deadlines[0] = 0
					case rdt.Send:
						if len(p.rdtPrefix) > 0 {
							if _, err = c.Write(p.rdtPrefix); err != nil {
								return
							}
							p.rdtPrefix = nil
						}
						if p.bulkBurst {
							select {
							case outgoing <- a.Data:
							case <-ctx.Done():
								return
							}
						} else if _, err = c.Write(a.Data); err != nil {
							return
						}
					case rdt.Ready:
						enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Start}})
					case rdt.Readable:
						enqueue(layerEvent{r: &rdt.Event{Kind: rdt.Read, Count: 8196}})
					case rdt.Bytes:
						if len(a.Data) > 0 {
							enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: a.Data}})
							enqueue(layerEvent{r: &rdt.Event{Kind: rdt.Read, Count: 8196}})
						}
					case rdt.Accepted:
						enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: a.Token, Count: a.Count}})
					case rdt.WriteCredit:
						enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.TransportWritable}})
					}
				}
			}
			if e.t != nil {
				if p.tunnelInput != nil {
					p.tunnelInput(*e.t, tm)
				}
				for _, a := range tm.Step(*e.t, now) {
					if p.socketScript != nil {
						events, handled := p.socketScript(a)
						for _, local := range events {
							enqueue(layerEvent{t: &local})
						}
						if handled {
							continue
						}
					}
					if bridge != nil && bridge.handle(a, enqueue) {
						continue
					}
					switch a.Kind {
					case p2ptunnel.ArmTimer:
						deadlines[1] = a.At
					case p2ptunnel.CancelTimer:
						deadlines[1] = 0
					case p2ptunnel.Send:
						enqueue(layerEvent{r: &rdt.Event{Kind: rdt.Write, Token: a.Token, Data: a.Data}})
						// Independent literal terminal fixtures after the final DATA.
						// These script the peer's wire outcome through real RDT/DTLS.
						if len(a.Data) >= 4 && a.Data[0] == 2 {
							ch := a.Data[1]
							if mode := finish[ch]; mode != "" && len(pending[ch]) == 0 {
								wire := []byte{4, ch, 1, 0, 0}
								if mode == "end" {
									wire = []byte{3, ch, 0, 0}
								}
								if mode == "fail" {
									wire[4] = 1
								}
								enqueue(layerEvent{r: &rdt.Event{Kind: rdt.Write, Data: wire}})
								delete(finish, ch)
							}
						}
					case p2ptunnel.ConnectLocal:
						httpChannels[a.Channel] = a.Port == 80 && len(p.httpResponse) > 0
						enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Connected, Channel: a.Channel, Generation: a.Generation}})
					case p2ptunnel.WriteLocal:

						received[a.Channel] = append(received[a.Channel], a.Data...)
						if len(received[a.Channel]) > 65536 {
							p.t.Error("unbounded application line")
							return
						}
						if httpChannels[a.Channel] {
							request, parseErr := http.ReadRequest(bufio.NewReader(bytes.NewReader(received[a.Channel])))
							if parseErr != nil || request.Method != http.MethodGet {
								p.t.Error("invalid standard HTTP fixture request", parseErr)
								return
							}
							request.Body.Close()
							pending[a.Channel] = append(pending[a.Channel], p.httpResponse...)
							received[a.Channel] = nil
						}
						for !httpChannels[a.Channel] {
							line, rest, ok := bytes.Cut(received[a.Channel], []byte{'\n'})
							if !ok {
								break
							}
							received[a.Channel] = rest
							if p.controlReply != nil {
								pending[a.Channel] = append(pending[a.Channel], p.controlReply(a.Channel, bytes.Clone(line))...)
								continue
							}
							var request struct {
								ID     int    `json:"id"`
								Method string `json:"method"`
								Finish string `json:"finish"`
							}
							if err := json.Unmarshal(line, &request, json.MatchCaseInsensitiveNames(true)); err != nil || request.Method != "get_device_state" {
								p.t.Error("invalid JSON-RPC request")
								return
							}
							// Synthetic response semantics: only line framing and transaction IDs
							// derive from the app appendix, never these invented state fields.
							response := fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":%d,\"result\":{\"state\":\"idle\"", request.ID)
							if p.responseBytes > 0 && request.ID == 99 {
								response += ",\"padding\":\"" + strings.Repeat("s", p.responseBytes) + "\""
							}
							pending[a.Channel] = append(pending[a.Channel], []byte(response+"}}\n")...)
							if request.Finish != "" {
								finish[a.Channel] = request.Finish
							}
						}
						e := event(a, p2ptunnel.LocalWritten)
						e.Count = len(a.Data)
						enqueue(layerEvent{t: &e})
						enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.ReadReady, Channel: a.Channel, Generation: a.Generation}})
					case p2ptunnel.ReadLocal:
						e := event(a, p2ptunnel.LocalData)
						n := min(p2ptunnel.MaxData, len(pending[a.Channel]))
						e.Data = pending[a.Channel][:n]
						pending[a.Channel] = pending[a.Channel][n:]
						if end[a.Channel] && len(e.Data) == 0 {
							e.Kind = p2ptunnel.LocalEOF
						}
						enqueue(layerEvent{t: &e})
						if p.bulkBurst && n > 0 {
							enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.ReadReady, Channel: a.Channel, Generation: a.Generation}})
						}
					case p2ptunnel.CloseLocal:
						delete(pending, a.Channel)
						delete(received, a.Channel)
						delete(end, a.Channel)
						delete(finish, a.Channel)
						delete(httpChannels, a.Channel)
					case p2ptunnel.ShutdownWrite:
						end[a.Channel] = true
						enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.ReadReady, Channel: a.Channel, Generation: a.Generation}})
					}
				}
			}
		}
		next := now.Add(time.Hour)
		for _, at := range deadlines {
			if at > 0 && at < next {
				next = at
			}
		}
		for _, b := range pending {
			if len(b) > 0 {
				next = min(next, now.Add(10*time.Millisecond))
			}
		}
		timer.Reset(max(time.Nanosecond, time.Duration(next-now)))
		for ch, b := range pending {
			if len(b) > 0 {
				enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.ReadReady, Channel: ch, Generation: tm.Channel(ch).Generation}})
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-completed:
			enqueue(layerEvent{r: &rdt.Event{Kind: rdt.SendComplete}})
		case err := <-writeFailed:
			if ctx.Err() == nil {
				p.t.Error(err)
			}
			return
		case e := <-localEvents:
			enqueue(layerEvent{t: &e})
		case b := <-inbound:
			q, err := rdt.Decode(b)
			if err != nil {
				p.t.Error(err)
				return
			}
			enqueue(layerEvent{r: &rdt.Event{Kind: rdt.Receive, Packet: q}})
		case <-timer.C:
			enqueue(layerEvent{r: &rdt.Event{Kind: rdt.Tick}})
			enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Tick}})
		}
	}
}

var _ io.Reader = (*pion.Conn)(nil)
