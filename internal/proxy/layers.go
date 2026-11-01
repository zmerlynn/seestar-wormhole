package proxy

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	mathrand "math/rand/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/edge"
	"github.com/zmerlynn/seestar-wormhole/internal/edge/iotcedge"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// New constructs the shared device owner from environment-only credentials.
// SEESTAR_EMAIL/SEESTAR_PASSWORD supply account login. New resolves the device
// from the account catalog before starting the owner, even when Config.Serial
// selects an entry; Config.Model disambiguates several models for that serial.
// The selected catalog or verified detail pair supplies both fields. Lazy mode still defers
// device-session allocation.
func New(ctx context.Context, cfg Config) (*Session, error) {
	c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Logger: diagnostic.Lifecycle(cfg.Logger), StateDir: cfg.StateDir})
	if err != nil {
		return nil, err
	}
	return newWithClient(ctx, cfg, c)
}

// NewWithCredentials uses in-memory credentials without changing the environment.
func NewWithCredentials(ctx context.Context, cfg Config, credentials cloud.Credentials) (*Session, error) {
	c, err := cloudhttp.NewWithCredentials(cloudhttp.Config{Logger: diagnostic.Lifecycle(cfg.Logger), StateDir: cfg.StateDir}, credentials)
	if err != nil {
		return nil, err
	}
	return newWithClient(ctx, cfg, c)
}

func newWithClient(ctx context.Context, cfg Config, c *cloudhttp.Client) (*Session, error) {
	c.SetCatalogObserver(cfg.Status.RecordCatalog)
	d, err := selectDevice(ctx, c, cloudhttp.Device{Serial: cfg.Serial, Model: cfg.Model}, cfg.Logger, mathrand.NewPCG(mathrand.Uint64(), mathrand.Uint64()))
	if err != nil {
		return nil, closeStartupCloud(ctx, c, err)
	}
	cfg.Status.SetSelectedDevice(d)
	var detail cloud.Detail
	if cfg.LANDiscovery {
		detail, err = c.DetailForLAN(ctx, d)
		if err != nil {
			return nil, closeStartupCloud(ctx, c, err)
		}
	}
	s, err := startSelected(ctx, cfg, detail, func(s *Session) sessionedge.Factory {
		s.deviceDetail = detail
		s.telescope = d.DisplayName()
		return &factory{s: s, cloud: c, device: d, verify: iotcedge.VerifyIOTCLicense, wire: func(l iotc.License) wireRunner { return &iotcedge.IOTCIO{Mask: l.Mask, Logger: cfg.Logger} }}
	})
	if err != nil {
		c.Close()
		return nil, err
	}
	return s, nil
}

func startSelected(ctx context.Context, cfg Config, detail cloud.Detail, factory func(*Session) sessionedge.Factory) (*Session, error) {
	if cfg.LANDiscovery {
		if _, _, err := landiscovery.New(detail.Serial(), detail.Name(), detail.Model()); err != nil {
			return nil, diagnostic.Wrap("lan_discovery", "invalid_device_detail", err)
		}
	}
	return start(ctx, cfg, factory)
}

type wireRunner interface {
	Run(context.Context, <-chan iotc.Action, func(iotc.Event) bool)
}
type factory struct {
	s      *Session
	cloud  *cloudhttp.Client
	device cloudhttp.Device
	verify func(string) (iotc.License, error)
	wire   func(iotc.License) wireRunner
}

func (f *factory) license(s string) (iotc.License, error) {
	l, err := f.verify(s)
	if errors.Is(err, iotc.ErrLicenseNUL) && f.s.logger != nil {
		f.s.logger.Warn("iotc_license_refused", "rule", "license_nul")
	}
	if err == nil && l.StandaloneOmitted > 0 && f.s.logger != nil {
		f.s.logger.Debug("iotc license standalone entries omitted", "count", l.StandaloneOmitted)
	}
	return l, err
}

// Close joins the account owner after all attempts have released their leases.
func (f *factory) Close() error { f.cloud.Close(); return nil }

func (f *factory) Work() <-chan func(core.Instant) { return f.s.Work() }
func (f *factory) New(id uint64, b session.Budgets) (sessionedge.Layers, error) {
	// Fixed maxima include machine queues, edge copies, and library admission.
	for _, l := range []session.Layer{session.Rendezvous, session.DTLS, session.RDT, session.Tunnel} {
		bytes, count := 8<<20, 1024
		if l == session.Tunnel {
			bytes, count = 80<<20, 12288
		} else if l == session.RDT {
			bytes, count = 16<<20, 16384
		}
		if b.Layers[l].Bytes < bytes || b.Layers[l].Count < count {
			return sessionedge.Layers{}, session.ErrBudget
		}
	}
	x := &stack{f: f, id: id, actions: make(chan iotc.Action, 1), secureTX: make(chan []byte, 16), timerUpdates: make(chan time.Duration, 1), streams: make(map[uint64]*stream)}
	if f.s != nil {
		f.s.discardStack.Store(x)
	}
	for i := range x.phases {
		x.phases[i] = make(chan error, 1)
	}
	for i := range x.handles {
		x.handles[i] = &layerHandle{x: x, notices: make(chan session.Event, 1)}
	}
	return sessionedge.Layers{Cloud: &allocator{f.cloud, f.device}, Rendezvous: x, DTLS: x, RDT: x, Tunnel: x}, nil
}

type allocator struct {
	c *cloudhttp.Client
	d cloudhttp.Device
}
type cloudLease struct{ *cloudhttp.Lease }

func (l cloudLease) Stop(context.Context) error {
	return cloudOutcome(cloud.ReleaseOutcome(l.Close()))
}
func (a *allocator) Allocate(ctx context.Context) (sessionedge.Lease, error) {
	// Joining the canceled account worker is teardown, just like lease stop.
	// The request still honors ctx; canceling that context cannot abandon its
	// eventual classification or authorize a replacement before it is known.
	joinCtx := context.WithoutCancel(ctx)
	if _, err := a.c.Detail(ctx, a.d); err != nil {
		return nil, cloudTerminal(cloudhttp.ResolveAccount(joinCtx, err))
	}
	l, err := a.c.Acquire(ctx, a.d)
	if err != nil {
		// Acquire has already joined all primary and bounded internal stop outcomes.
		// The adapter cannot select one cause and discard a more severe observation.
		return nil, cloudTerminal(cloudhttp.ResolveAccount(joinCtx, err))
	}
	return cloudLease{l}, nil
}

type layerHandle struct {
	x       *stack
	notices chan session.Event
}

func (h *layerHandle) Events() <-chan session.Event { return h.notices }
func (h *layerHandle) Close() error                 { return h.x.close() }
func (h *layerHandle) TryOpen(id uint64) error {
	x := h.x
	x.f.s.mu.Lock()
	c := x.f.s.clients[id]
	x.f.s.mu.Unlock()
	if c == nil {
		return session.ErrClosed
	}
	if x.ctx.Err() != nil {
		return session.ErrClosed
	}
	select {
	case <-c.done:
		return session.ErrClosed
	default:
	}
	if len(x.openAdmissions) >= pendingOpenLimit {
		return session.ErrBudget
	}
	if x.openAdmissions == nil {
		x.openAdmissions = make(map[uint64]core.Instant)
	}
	x.openAdmissions[id] = x.now.Add(pendingOpenWait)
	x.streams[id] = c
	c.step = func(e p2ptunnel.Event, now core.Instant) { x.drive(layerEvent{t: &e}, now) }
	x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Open, Request: id, Port: c.port}}, x.now)
	return nil
}
func (h *layerHandle) TryClose(id uint64) {
	x := h.x
	if c := x.streams[id]; c != nil {
		for ch, entry := range x.channels {
			if entry == c {
				x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.LocalClose, Channel: byte(ch), Generation: x.tunnel.Channel(byte(ch)).Generation}}, x.now)
			}
		}
		delete(x.openAdmissions, id)
		x.pending = slices.DeleteFunc(x.pending, func(v uint64) bool { return v == id })
		x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.CancelOpen, Request: id}}, x.now)
		delete(x.streams, id)
		c.finish(session.ErrClosed)
	}
}

// stack state below is accessed only by the session owner, except explicitly
// worker-owned resources (ctx/cancel/wg, secure, packet queues and phase channels).
type stack struct {
	ingressDiscards    uint64
	discardNext        core.Instant
	discardLast        [132]uint64
	f                  *factory
	id                 uint64
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	once               sync.Once
	handles            [4]*layerHandle
	phases             [3]chan error
	actions            chan iotc.Action
	packets            *edge.DTLSPackets
	secure             *edge.SecureDTLS
	username, password []byte
	secureTX           chan []byte
	timerUpdates       chan time.Duration
	iotc               *iotc.Machine
	reliable           *rdt.Machine
	tunnel             *p2ptunnel.Machine
	deadlines          [4]core.Instant
	pending            []uint64
	openAdmissions     map[uint64]core.Instant // owner-only, absolute deadlines for not-yet-open clients
	now                core.Instant
	queue              []layerEvent
	effects            []iotc.Action
	driving, failed    bool
	terminal           atomic.Pointer[diagnostic.Terminal] // owner publishes once; constructor workers only load
	streams            map[uint64]*stream
	channels           [256]*stream
	wantRead           [256]bool
}

func (x *stack) post(fn func(core.Instant)) bool {
	select {
	case <-x.ctx.Done():
		return false
	default:
	}
	select {
	case x.f.s.work <- func(now core.Instant) {
		if x.ctx.Err() == nil {
			fn(now)
		}
	}:
		return true
	case <-x.ctx.Done():
		return false
	}
}

// terminalCause reads the immutable cause published by the owner before waking
// constructor workers. Caller cancellation cannot erase an observed failure.
func (x *stack) terminalCause(fallback error) error {
	if cause := x.terminal.Load(); cause != nil {
		return cause
	}
	return fallback
}

func (x *stack) wait(ctx context.Context, n int) error {
	select {
	case err := <-x.phases[n]:
		return x.terminalCause(err)
	case <-ctx.Done():
		return x.terminalCause(ctx.Err())
	case <-x.ctx.Done():
		return x.terminalCause(session.ErrTransport)
	}
}

func (x *stack) close() error {
	x.once.Do(func() {
		if x.cancel != nil {
			x.cancel()
		}
		if x.packets != nil {
			x.packets.Close()
		}
		if x.secure != nil {
			x.secure.Close()
		}
		x.wg.Wait()
		if x.f.s.logger != nil {
			for _, phase := range []string{"tunnel", "rdt", "dtls", "rendezvous"} {
				x.f.s.logger.Debug("phase_transition", "phase", phase, "state", "closed")
			}
		}
	})
	return nil
}
func (x *stack) Dial(ctx context.Context, l sessionedge.Lease) (result sessionedge.Transport, err error) {
	finish := diagnostic.Operation(diagnostic.Lifecycle(x.f.s.logger), "rendezvous")
	defer func() { finish(err) }()
	lease, ok := l.(cloudLease)
	if !ok {
		return nil, sessionedge.ErrAdapter
	}
	a := lease.Allocation()
	license, err := x.f.license(string(a.License()))
	if err != nil {
		return nil, diagnostic.Wrap("rendezvous", "invalid_license", err)
	}
	uid := a.UID()
	if len(uid) == 0 || len(uid) > 20 {
		return nil, diagnostic.Wrap("rendezvous", "invalid_identifier", session.ErrTransport)
	}
	var target [20]byte
	copy(target[:], uid)
	var b [4]byte
	if _, err = rand.Read(b[:]); err != nil {
		return nil, session.ErrTransport
	}
	sid := binary.LittleEndian.Uint32(b[:])
	if sid == 0xffff {
		sid = 0
	}
	m, err := iotc.New(iotc.Config{Attempt: x.id, RecordSID: sid, UID: target, License: license})
	if err != nil {
		return nil, session.ErrTransport
	}
	x.ctx, x.cancel = context.WithCancel(ctx)
	x.username = []byte(a.Username())
	x.password = []byte(a.Password())
	x.packets, err = edge.NewDTLSPackets(1388)
	if err != nil {
		return x.handles[0], err
	}
	x.packets.Logger = x.f.s.logger
	x.packets.EnableWriteCompletion()
	x.post(func(now core.Instant) {
		x.iotc = m
		x.drive(layerEvent{i: &iotc.Event{Kind: iotc.Start, Attempt: x.id}}, now)
	})
	wireActions := make(chan iotc.Action)
	x.wg.Go(func() {
		for {
			select {
			case <-x.ctx.Done():
				return
			case a := <-x.actions:
				select {
				case wireActions <- a:
				case <-x.ctx.Done():
					return
				}
				if !x.post(func(now core.Instant) { x.drive(layerEvent{}, now) }) {
					return
				}
			}
		}
	})
	x.wg.Go(func() {
		x.f.wire(license).Run(x.ctx, wireActions, func(e iotc.Event) bool {
			e.Attempt = x.id
			if e.Kind == iotc.Sent {
				return x.post(func(n core.Instant) {
					if e.Token == 1 {
						x.packets.CompleteWrite(nil)
					}
				})
			}
			return x.post(func(n core.Instant) { x.drive(layerEvent{i: &e}, n) })
		})
	})
	x.wg.Go(x.timers)
	x.wg.Go(func() {
		for {
			select {
			case <-x.ctx.Done():
				return
			case b := <-x.packets.Outgoing():
				e := iotc.Event{Kind: iotc.Write, Attempt: x.id, Token: 1, Data: b}
				if !x.post(func(n core.Instant) {
					x.drive(layerEvent{i: &e}, n)
				}) {
					return
				}
			}
		}
	})
	return x.handles[0], x.wait(ctx, 0)
}
func (x *stack) Handshake(ctx context.Context, _ sessionedge.Transport) (result sessionedge.Transport, err error) {
	finish := diagnostic.Operation(diagnostic.Lifecycle(x.f.s.logger), "dtls")
	defer func() {
		finish(err)
	}()
	// The lease is retained by the attempt worker; obtain credentials there via
	// Dial's short-lived copy, cleared immediately after this handshake.
	s, err := edge.DialDTLS(ctx, x.packets, x.username, x.password)
	clear(x.username)
	clear(x.password)
	x.username = nil
	x.password = nil
	if err != nil {
		reason := diagnostic.Class(err)
		if reason == "operation_failed" {
			reason = "handshake_failed"
		}
		return x.handles[1], x.terminalCause(diagnostic.Wrap("dtls", reason, err))
	}
	x.secure = s
	if x.f.s.logger != nil {
		p := s.Profile()
		x.f.s.logger.Info("dtls_ready", "version", p.Version, "suite", p.Suite)
		x.f.s.logger.Debug("phase_transition", "phase", "dtls", "state", "negotiated", "version", p.Version, "suite", p.Suite)
	}
	return x.handles[1], nil
}

// runtimeRDTConfig accounts for the documented native maximum send window.
// Window size is local and not negotiated credit; this receive envelope is proxy policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt/reliability.md#window-and-pacing
func runtimeRDTConfig() rdt.Config {
	cfg := rdt.DefaultConfig()
	cfg.TXPackets = 256
	cfg.SendCapacity = 16
	// The device can send up to 6500 packets without negotiated receive credit.
	// Hold a full window plus its head
	// reservation; larger bursts remain recoverable unacknowledged loss.
	cfg.RXPackets = 8192
	cfg.RXBytes = 8 << 20
	return cfg
}

func (x *stack) Create(ctx context.Context, _ sessionedge.Transport) (result sessionedge.Transport, err error) {
	finish := diagnostic.Operation(diagnostic.Lifecycle(x.f.s.logger), "rdt")
	defer func() { finish(err) }()
	cfg := runtimeRDTConfig()
	m, err := rdt.New(cfg)
	if err != nil {
		return nil, err
	}
	x.post(func(now core.Instant) {
		x.reliable = m
		x.drive(layerEvent{r: &rdt.Event{Kind: rdt.Start}}, now)
	})
	x.wg.Go(x.secureRead)
	x.wg.Go(x.secureWrite)
	return x.handles[2], x.wait(ctx, 1)
}
func (x *stack) Start(ctx context.Context, _ sessionedge.Transport) (result sessionedge.Channels, err error) {
	finish := diagnostic.Operation(diagnostic.Lifecycle(x.f.s.logger), "tunnel")
	defer func() { finish(err) }()
	m, err := p2ptunnel.New(x.f.s.tunnelConfig)
	if err != nil {
		return nil, err
	}
	x.post(func(now core.Instant) {
		x.tunnel = m
		x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Start}}, now)
		x.drive(layerEvent{r: &rdt.Event{Kind: rdt.Read, Count: p2ptunnel.MaxInput}}, now)
	})
	return x.handles[3], x.wait(ctx, 2)
}

// runtimeTunnelConfig is local policy; thresholds are not negotiated credit.
// Proxy high water and channel budgets supplement the unresolved peer burst:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/channels.md#backlog-after-pause
func runtimeTunnelConfig(threshold int) p2ptunnel.Config {
	cfg := p2ptunnel.DefaultConfig()
	cfg.MaxChannels = 128
	if threshold != 0 {
		cfg.HighWater = threshold
	}
	return cfg
}
