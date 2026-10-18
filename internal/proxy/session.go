package proxy

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// MaxPauseThreshold retains at least 8 MiB of channel burst headroom.
const MaxPauseThreshold = 8 << 20

// Config is local session policy. Zero Idle holds a session eagerly; a positive
// Idle allocates on first demand and releases after the last client leaves.
type Config struct {
	// Trace is opt-in; the caller closes it after Session.Close.
	Trace *tracerecord.Writer
	// LANDiscovery loads selected cloud identity before starting the owner.
	// This requests detail only; even in lazy mode it makes no allocation.
	LANDiscovery bool
	// Serial optionally selects a device; Model disambiguates a serial with several
	// catalog models. StateDir opts into installation UUID persistence.
	Serial, Model, StateDir string
	// HoldOnDemand defers acquisition until DialPort and holds until Close. Pull
	// uses this to bind destination identity before acquiring the device.
	HoldOnDemand bool
	// BusyWait bounds initial acquisition and occupancy for HoldOnDemand, shared
	// across backend retries. After acquisition, transport recovery may outlive
	// this budget; recovery busy answers never renew it. Zero fails fast on busy.
	BusyWait time.Duration
	// KeyLog is opt-in; the caller closes it after Session.Close.
	KeyLog *keylog.Writer
	// PauseThreshold is bytes of attributed receive pressure; zero uses the default.
	PauseThreshold int
	Idle           time.Duration
	Logger         *slog.Logger
	// StatsInterval enables transport telemetry; zero disables it. The command defaults to one second.
	StatsInterval time.Duration
}

// Session owns one device allocation and its bounded stream clients. Use New to
// construct one per device, and always Close it, including after a failed dial.
// Protocol state is owned by sessionedge.Run; mu protects only API registrations.
type Session struct {
	discardStack             atomic.Pointer[stack] // published by the attempt worker; drained by the owner after join
	discardNext              core.Instant
	discardLast              uint64
	telemetryLost            uint64 // owner-only until done closes
	trace                    *tracerecord.Writer
	traceID                  uint64
	sourceID                 string       // immutable opaque selected-device identity
	deviceDetail             cloud.Detail // immutable after construction, before publication
	tunnelConfig             p2ptunnel.Config
	statsInterval            time.Duration
	lazy                     bool
	keylog                   *keylog.Writer
	logger                   *slog.Logger
	lifecycleReason          string // owner-only fixed vocabulary
	wasReady                 bool   // owner-only availability telemetry
	unavailable              bool
	unavailableSince, warnAt core.Instant
	busyAnswers              uint64
	terminalFailure          error // mu: typed attribution retained for client outcomes
	mu                       sync.Mutex
	clients                  map[uint64]*stream
	serial                   uint64
	err                      error
	in                       chan session.Event
	work                     chan func(core.Instant)
	done                     chan struct{}
	cancel                   context.CancelFunc
}

func start(ctx context.Context, cfg Config, factory func(*Session) sessionedge.Factory) (*Session, error) {
	return startWithRNG(ctx, cfg, factory, rand.NewPCG(rand.Uint64(), rand.Uint64()))
}

func startWithRNG(ctx context.Context, cfg Config, factory func(*Session) sessionedge.Factory, rng session.RNG) (*Session, error) {
	if cfg.Idle < 0 {
		return nil, &diagnostic.OptionError{Option: "--idle", Rule: "duration must be nonnegative"}
	}
	if cfg.StatsInterval < 0 {
		return nil, &diagnostic.OptionError{Option: "--stats-interval", Rule: "duration must be nonnegative"}
	}
	if cfg.BusyWait < 0 {
		return nil, &diagnostic.OptionError{Option: "--busy-wait", Rule: "use a nonnegative Go duration"}
	}
	if cfg.PauseThreshold < 0 || cfg.PauseThreshold == 1 || cfg.PauseThreshold > MaxPauseThreshold {
		return nil, &diagnostic.OptionError{Option: "--pause-threshold", Rule: "byte count must be 2..8388608 (zero selects default)"}
	}
	if cfg.HoldOnDemand {
		cfg.Idle = time.Duration(math.MaxInt64)
	}
	s := &Session{lazy: cfg.Idle > 0, trace: cfg.Trace, traceID: cfg.Trace.Instance(), statsInterval: cfg.StatsInterval, tunnelConfig: runtimeTunnelConfig(cfg.PauseThreshold), keylog: cfg.KeyLog, logger: cfg.Logger, clients: make(map[uint64]*stream), in: make(chan session.Event, 257), work: make(chan func(core.Instant), 128), done: make(chan struct{})}
	ctx, s.cancel = context.WithCancel(ctx)
	c := session.DefaultConfig()
	c.Idle = cfg.Idle
	c.LazyBusyWindow = session.LazyBusyWindow
	c.RetryBusy = !cfg.HoldOnDemand
	if cfg.HoldOnDemand {
		c.LazyBusyWindow = cfg.BusyWait
		c.SingleBusyWindow = true
	}
	c.MaxClients = 128
	c.DialTimeout = 120 * time.Second
	// The work queue can hold 128 byte completions of at most 8192 bytes, in
	// addition to fixed-size commands/results. All are covered by this reservation.
	c.Budgets.Inbox.Bytes = 2 << 20
	// Tunnel payload (64 MiB), bounded chunk metadata, TX/read reservations and
	// edge deliveries fit this layer reservation. Per-channel adapter storage
	// remains separately reserved; unused receive capacity is never allocated.
	c.Budgets.Layers[session.Tunnel] = session.Limit{Bytes: 80 << 20, Count: 12288}
	c.Budgets.Layers[session.RDT] = session.Limit{Bytes: 16 << 20, Count: 16384}
	c.Budgets.Total.Bytes = 160 << 20
	c.Budgets.Total.Count = 40960
	f := factory(s)
	go func() {
		err := sessionedge.Run(ctx, c, rng, f, s.in, s)
		if closer, ok := f.(io.Closer); ok {
			err = errors.Join(err, closer.Close())
		}
		s.mu.Lock()
		s.err = err
		for _, c := range s.clients {
			c.finish(session.ErrClosed)
		}
		s.clients = make(map[uint64]*stream)
		close(s.done)
		s.mu.Unlock()
	}()
	return s, nil
}

// TraceRecorder supplies the existing owner with its optional recorder.
func (s *Session) TraceRecorder() (*tracerecord.Writer, uint64) { return s.trace, s.traceID }

// Work implements the session owner's bounded completion driver.
func (s *Session) Work() <-chan func(core.Instant) { return s.work }

// availabilityError marks owner rejections already diagnosed by lifecycle logs;
// channel admission failures keep their individual listener diagnostics.
type availabilityError struct{ error }

func (e *availabilityError) Unwrap() error { return e.error }

// Try implements nonblocking delivery of session admission outcomes.
func (s *Session) Try(a session.Action) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clients[a.Client]
	if c != nil && (a.Kind == session.Reject || a.Kind == session.Disconnect) {
		cause := a.Err
		// Release bookkeeping can strengthen uncertain ownership to ErrStop.
		// Keep the received cause when projecting that stronger blocking policy.
		if cause != nil && s.terminalFailure != nil && (errors.Is(s.terminalFailure, cause) || cause == session.ErrStop && errors.Is(s.terminalFailure, session.ErrUncertain)) {
			cause = diagnostic.RetainTerminal(s.terminalFailure, cause)
		}
		if a.Kind == session.Reject && cause != nil && !errors.Is(cause, session.ErrBudget) {
			cause = &availabilityError{cause}
		}
		c.finish(cause)
	}
	return nil
}

// DialPort opens one tunnel channel to a device TCP port. The returned net.Conn
// also implements interface{ CloseWrite() error }. ctx bounds admission only;
// connection deadlines and Close control the stream after a successful dial.
func (s *Session) DialPort(ctx context.Context, port uint16) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if port == 0 {
		return nil, errors.New("proxy: invalid device port")
	}
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		return nil, session.ErrClosed
	default:
	}
	if len(s.clients) >= 128 {
		s.mu.Unlock()
		return nil, session.ErrBudget
	}
	s.serial++
	c := newStream(s, s.serial, port)
	s.clients[c.id] = c
	// Each registration reserves two inbox slots until ClientGone is consumed.
	// Submission cannot block, and registration never precedes an unsent Client.
	select {
	case s.in <- session.Event{Kind: session.Client, Client: c.id}:
	default:
		delete(s.clients, c.id)
		s.mu.Unlock()
		return nil, session.ErrBudget
	}
	s.mu.Unlock()
	select {
	case err := <-c.result:
		// A successful result and cancellation can become ready together.
		// Resolve observable cancellation before handing ownership to the caller.
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			c.Close()
			return nil, err
		}
		return c, nil
	case <-ctx.Done():
		c.Close()
		return nil, ctx.Err()
	case <-s.done:
		c.Close()
		return nil, session.ErrClosed
	}
}

// Close cancels clients and joins local cleanup and the bounded cloud stop.
func (s *Session) Close() error {
	s.cancel()
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.err
	var terminal *diagnostic.Terminal
	if errors.Is(err, context.Canceled) && !errors.Is(err, session.ErrStop) && !errors.As(err, &terminal) {
		err = nil
	}
	if s.telemetryLost > 0 {
		err = errors.Join(err, &TelemetryLossError{Count: s.telemetryLost})
	}
	return err
}

// Done closes after local cleanup and the cloud stop attempt finish.
func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) forget(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.clients[id]
	if c == nil || c.forgotten {
		return
	}
	c.forgotten = true
	// Keep the registration until the owner consumes this command. Its reserved
	// slot makes this send nonblocking even when the owner itself calls forget.
	select {
	case s.in <- session.Event{Kind: session.ClientGone, Client: id}:
	default:
		panic("proxy: lost cleanup reservation")
	}
}

// Observe returns consumed command reservations and logs bounded lifecycle transitions.
func (s *Session) Observe(e session.Event, before, after session.Snapshot, now core.Instant) {
	s.sessionDiscardSummary(after.Discards, now)
	// A normal release matches the releasing generation. Final cleanup uses
	// identical snapshots and a zero attempt; stale Released events must not
	// drain diagnostics belonging to the active generation.
	released := e.Kind == session.Released && (e.Attempt == before.Attempt && before.State == session.Releasing || e.Attempt == 0 && before == after)
	if released {
		// Released is observed only after the attempt's workers have joined.
		if x := s.discardStack.Swap(nil); x != nil {
			x.flushDiscardSummary()
			s.telemetryLost += x.debugDropped.Load()
		}
	}
	if released || after.State == session.Closed {
		s.flushSessionDiscardSummary(after.Discards)
	}
	switch e.Kind {
	case session.ClientGone:
		s.mu.Lock()
		delete(s.clients, e.Client)
		s.mu.Unlock()
	}
	if e.Kind == session.Failed || after.State == session.Ready {
		s.mu.Lock()
		if e.Kind == session.Failed {
			s.terminalFailure = diagnostic.Primary(e.Err)
		} else {
			s.terminalFailure = nil
		}
		s.mu.Unlock()
	}
	if s.logger == nil {
		return
	}
	if after.State == session.Ready {
		s.wasReady = true
		s.unavailable = false
		s.busyAnswers = 0
	} else if after.State == session.Closed {
		s.unavailable = false
	} else if !s.unavailable && ((before.State == session.Ready && (e.Kind == session.Failed || e.Kind == session.Usage)) || (s.wasReady && after.State == session.Busy) || (!s.lazy && e.Kind == session.Failed && errors.Is(e.Err, session.ErrBusy))) {
		s.unavailable = true
		s.unavailableSince = now
		s.warnAt = now.Add(5 * time.Minute)
	}
	if before.Attempt != after.Attempt && after.State == session.Dialing && s.lazy {
		cause := "client_demand"
		if before.State == session.Backoff {
			cause = s.lifecycleReason
		}
		s.logger.Info("lazy_open_attempt", "cause", cause)
	}
	if e.Kind == session.Failed {
		phase, cause := diagnostic.Fields(e.Err, "session")
		if cause != "canceled" {
			if cause == "busy" && !errors.Is(e.Err, session.ErrStop) && !errors.Is(e.Err, session.ErrUncertain) && !errors.Is(e.Err, session.ErrReleaseAbandoned) {
				s.busyAnswers++
				s.logger.Info("layer_failed", "phase", phase, "reason", cause)
			} else {
				diagnostic.Log(s.logger, "layer_failed", "session", e.Err)
			}
		}
	}
	reason := "none"
	switch {
	case errors.Is(e.Err, session.ErrReleaseAbandoned):
		reason = "release_abandoned"
	case errors.Is(e.Err, session.ErrBusy):
		reason = "busy"
	case errors.Is(e.Err, session.ErrStop):
		reason = "release_failed"
	case errors.Is(e.Err, session.ErrUncertain):
		reason = "ownership_uncertain"
	case errors.Is(e.Err, session.ErrTimeout):
		reason = "timeout"
	case e.Err != nil:
		reason = "transport"
	case before.State == session.Ready && after.State == session.Releasing:
		reason = "idle"
	}
	var terminal *diagnostic.Terminal
	if errors.As(e.Err, &terminal) {
		_, reason = diagnostic.Fields(e.Err, "")
	} else if e.Kind == session.Released && (errors.Is(e.Err, session.ErrUncertain) || errors.Is(e.Err, session.ErrStop)) {
		// A policy-only release receipt must not erase the received reason
		// that already blocked this attempt. Actual cleanup policy still wins.
		s.mu.Lock()
		if errors.Is(s.terminalFailure, session.ErrUncertain) || errors.Is(s.terminalFailure, session.ErrStop) {
			_, reason = diagnostic.Fields(s.terminalFailure, "")
		}
		s.mu.Unlock()
	}
	if reason != "none" {
		s.lifecycleReason = reason
	}
	// The owner arms a separate observation deadline, so terminal ownership
	// blocks and in-flight dials still report without incoming client events.
	if s.unavailable && after.State != session.Closed && now >= s.warnAt {
		s.logger.Warn("session_unavailable", "reason", s.lifecycleReason, "unavailable_ms", time.Duration(now-s.unavailableSince).Milliseconds(), "busy_answers", s.busyAnswers)
		s.warnAt = now.Add(5 * time.Minute)
	}
	if before.State == after.State && e.Kind != session.Progress && e.Kind != session.Released {
		return
	}
	phases := [...]string{session.Dormant: "dormant", session.Dialing: "dialing", session.Ready: "ready", session.Releasing: "releasing", session.Backoff: "backoff", session.Busy: "busy", session.Closed: "closed"}
	phase := "closed"
	if int(after.State) < len(phases) {
		phase = phases[after.State]
	}
	event := "session_state"
	if e.Kind == session.Released {
		event = "session_release_complete"
	}
	if e.Kind == session.Progress {
		event = "session_phase_complete"
	}
	layers := [...]string{"cloud", "rendezvous", "dtls", "rdt", "tunnel", "stack"}
	layer := "none"
	if (e.Kind == session.Progress || e.Kind == session.Failed) && int(e.Layer) < len(layers) {
		layer = layers[e.Layer]
	}
	retry := int64(0)
	if after.State == session.Backoff {
		retry = max(0, int64(time.Duration(after.Deadline-now)/time.Millisecond))
	}
	clients := "wait"
	if after.State == session.Ready {
		clients = "forward"
	} else if after.State == session.Busy || after.State == session.Closed || (after.State == session.Releasing && after.Queued == 0) || (!s.lazy && (s.wasReady || s.unavailable)) {
		clients = "accept_close"
	}
	s.logger.Info(event, "phase", phase, "layer", layer, "reason", s.lifecycleReason, "retry_ms", retry, "local_clients", clients)
	if after.State == session.Ready {
		s.lifecycleReason = "none"
	}
}

// DeviceDetail returns selected cloud identity when LANDiscovery was enabled.
// Values are for the discovery wire, never logs; Detail redacts formatting.
func (s *Session) DeviceDetail() cloud.Detail { return s.deviceDetail }

// SourceID returns the opaque selected-device identity without another cloud call.
func (s *Session) SourceID() string { return s.sourceID }

// ObservationDeadline requests the next unavailable or discard summary on the owner clock.
func (s *Session) ObservationDeadline() core.Instant {
	if s.logger == nil {
		return 0
	}
	next := s.discardNext
	if s.unavailable && (next == 0 || s.warnAt < next) {
		next = s.warnAt
	}
	return next
}
