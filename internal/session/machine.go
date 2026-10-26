package session

import (
	"errors"
	"math"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

type clientSlot struct {
	id             uint64
	used, admitted bool
}

// Snapshot exposes bounded accounting, never mutable machine storage.
type Snapshot struct {
	Discards          uint64 // cumulative R097 stale/irrelevant event count
	State             State
	OwnershipRecovery bool // An ownership block is waiting for cleanup or replacement.
	Attempt           uint64
	Clients, Queued   int
	Usage             Limit
	Deadline          core.Instant
}

// Machine is owned by one caller. Allocate one per device and do not copy it.
// Each Dial requires fresh layer machines and an independent context at the edge.
type Machine struct {
	discards                         uint64
	cfg                              Config
	rng                              RNG
	state, after                     State
	started                          bool
	closeDuringRecovery              bool
	busyCloud                        bool
	lastDial                         core.Instant
	attempt                          uint64
	next                             Layer
	clients                          []clientSlot
	count, queued                    int
	usage                            [LayerCount]Limit
	last, deadline, until, idleUntil core.Instant
	retry                            time.Duration
	blockErr                         error
	permanent                        bool
	retryAt                          core.Instant
	retryPending                     bool
	refusalRetry                     bool
	recentUntil                      core.Instant
	releasedAttempt                  uint64
	lazyUntil                        core.Instant
	lazyRetry                        time.Duration

	releaseOverdue bool
	releaseRefusal bool // Retryable refusal cleanup still owns its release obligation.
}

// New validates and reserves the entire configured resource envelope up front.
func New(c Config, rng RNG) (*Machine, error) {
	if _, err := c.Reservation(); err != nil {
		return nil, err
	}
	if rng == nil || c.RecentReleaseWindow < 0 || c.LazyBusyWindow < 0 || c.Idle < 0 || c.DialTimeout <= 0 || c.StopTimeout <= 0 || c.BackoffMin <= 0 || c.BackoffMax < c.BackoffMin || c.MaxQueued < 1 || c.MaxQueued > c.MaxClients {
		return nil, ErrBudget
	}
	return &Machine{cfg: c, rng: rng, clients: make([]clientSlot, c.MaxClients), retry: firstBackoff(c)}, nil
}

// Start with a non-degenerate jitter range even after a healthy session fails.
func firstBackoff(c Config) time.Duration {
	if c.BackoffMin > c.BackoffMax/2 {
		return c.BackoffMax
	}
	return c.BackoffMin * 2
}

// Snapshot reports layer usage plus fixed channel reservations for live clients.
func (m *Machine) Snapshot() Snapshot {
	u := Limit{m.count * m.cfg.Budgets.Channel.Bytes, m.count * m.cfg.Budgets.Channel.Count}
	for _, l := range m.usage {
		u, _ = addLimit(u, l)
	}
	return Snapshot{State: m.state, OwnershipRecovery: m.state != Closed && m.ownershipRecovery(), Attempt: m.attempt, Clients: m.count, Queued: m.queued, Usage: u, Deadline: m.deadline, Discards: m.discards}
}
func (m *Machine) action(k Effect) Action { return Action{Kind: k, Attempt: m.attempt} }
func (m *Machine) clientAction(k Effect, id uint64, err error) Action {
	a := m.action(k)
	a.Client = id
	a.Err = err
	return a
}
func after(now core.Instant, d time.Duration) core.Instant {
	if now > core.Instant(math.MaxInt64)-core.Instant(d) {
		return core.Instant(math.MaxInt64)
	}
	return now.Add(d)
}
func (m *Machine) lazyBusyEnabled() bool { return m.cfg.Idle > 0 && m.cfg.LazyBusyWindow > 0 }
func (m *Machine) ambientRecovery() bool {
	return m.cfg.Idle == 0 && m.cfg.RetryBusy
}

func (m *Machine) resetLazyOpen() {
	m.lazyUntil = 0
	m.lazyRetry = 0
	m.refusalRetry = false
	m.retryAt = 0
	m.retryPending = false
}

func (m *Machine) dial(now core.Instant) []Action {
	// A queued refusal retry retains its authorization deadline across timers,
	// canceled demand. Every dial path must recheck it.
	if m.refusalRetry && !m.lazyBusyEnabled() && !m.ambientRecovery() && now >= m.recentUntil {
		m.state = Busy
		m.blockErr = ErrBusy
		return m.drop(ErrBusy)
	}
	// Fresh lazy demand cannot erase the minimum interval by canceling an open
	// or exhausting its busy budget. Keep this floor independent of that budget.
	if m.lazyBusyEnabled() && m.attempt != 0 && now < after(m.lastDial, m.cfg.BackoffMin) {
		m.state = Backoff
		m.until = after(m.lastDial, m.cfg.BackoffMin)
		return nil
	}

	if m.attempt == math.MaxUint64 || now == core.Instant(math.MaxInt64) {
		m.state = Closed
		return append(m.drop(ErrClosed), m.action(Stopped))
	}
	m.blockErr = nil
	m.permanent = false
	m.attempt++
	m.lastDial = now
	m.state = Dialing
	m.next = Cloud
	m.until = after(now, m.cfg.DialTimeout)
	if m.lazyBusyEnabled() && m.count > 0 {
		if m.lazyUntil == 0 {
			m.lazyUntil = after(now, m.cfg.LazyBusyWindow)
		}
		m.until = min(m.until, m.lazyUntil)
	}
	m.usage = [LayerCount]Limit{}
	return []Action{m.action(Dial)}
}
func (m *Machine) drop(err error) (out []Action) {
	for i, c := range m.clients {
		if c.used {
			k := Reject
			if c.admitted {
				k = Disconnect
			}
			out = append(out, m.clientAction(k, c.id, err))
			m.clients[i] = clientSlot{}
		}
	}
	m.count = 0
	m.queued = 0
	m.idleUntil = 0
	// The budget belongs to waiting demand, not the worker or retry history.
	// Rejection needs no later ClientGone to retire it.
	m.lazyUntil = 0
	return out
}

// Transport teardown and stop are distinct; local cleanup must join before replacement:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/state-timers.md#app-workflow
func (m *Machine) release(target State, err error, now core.Instant) []Action {
	// A ready session is ours even when transport loss ends it before cloud
	// cleanup. A confirmed stop below reanchors the window at stop completion.
	// Failed/uncertain cleanup delays replacement until after the worker joins.
	if m.state == Ready {
		m.recentUntil = after(now, m.cfg.RecentReleaseWindow)
		m.releasedAttempt = m.attempt
	}
	var out []Action
	// Keep bounded queued demand across retryable refusal cleanup so one lazy
	// caller can recover without reconnecting. Ambient retries need no demand;
	// close even clients that arrived before the first busy answer.
	if !(m.state == Dialing && target == Backoff && err == ErrBusy && !m.ambientRecovery()) {
		out = m.drop(err)
	}
	if target == Backoff && m.state != Releasing {
		m.retryPending = true
	}
	m.after = target
	if m.state == Releasing {
		return out
	}
	if m.state == Dialing || m.state == Ready {
		m.releaseRefusal = (m.lazyBusyEnabled() || m.ambientRecovery()) && m.refusalRetry
		m.state = Releasing
		m.releaseOverdue = false
		m.until = after(now, m.cfg.StopTimeout)
		return append(out, m.action(Release))
	}
	m.state = target
	return append(out, m.settle(now)...)
}
func (m *Machine) ownershipRecovery() bool {
	return !m.permanent && (m.blockErr == ErrStop || m.blockErr == ErrUncertain)
}

func (m *Machine) settle(now core.Instant) []Action {
	switch m.state {
	case Closed:
		a := m.action(Stopped)
		if m.blockErr == ErrStop || m.blockErr == ErrUncertain {
			a.Err = m.blockErr
		}
		return []Action{a}
	case Busy:
		if m.ownershipRecovery() {
			// Reuse the retry floor and the eager admission policy after readiness.
			// Retain the failure for shutdown in the delay.
			m.closeDuringRecovery = true
			m.retryPending = false
			m.refusalRetry = false
			m.retryAt = after(now, OwnershipRecoveryDelay)
			m.state = Backoff
			m.until = m.retryAt
		}
	case Dormant:
		if m.blockErr != nil && !m.ownershipRecovery() {
			m.state = Busy
			return nil
		}
		if now < m.retryAt {
			m.state = Backoff
			m.until = m.retryAt
			return nil
		}
		if m.started && (m.cfg.Idle == 0 || m.count > 0) {
			return m.dial(now)
		}
	case Backoff:
		m.state = Dormant
		return m.settle(now)
	}
	return nil
}

// Retry history belongs to the attempt outcome, independently of demand or
// commands that change the post-release state while cleanup is pending.
func (m *Machine) backoff(now core.Instant) {
	var delay time.Duration
	if m.lazyUntil != 0 && m.refusalRetry {
		// Separate history leaves eager/transport recovery unchanged. Equal jitter
		// starts at 2–4s and doubles its cap to 15s, avoiding synchronized polling.
		delay, m.lazyRetry = RetryBackoff(Config{BackoffMin: 2 * time.Second, BackoffMax: 15 * time.Second}, m.rng, m.lazyRetry)
	} else {
		delay, m.retry = RetryBackoff(m.cfg, m.rng, m.retry)
	}
	m.retryAt = after(now, delay)
	if m.lazyUntil != 0 && m.refusalRetry {
		m.retryAt = min(m.retryAt, m.lazyUntil)
	}
	m.retryPending = false
}

// RetryBackoff returns equal jitter and the next exponential cap for session and
// startup cloud recovery. c must have validated positive backoff bounds and rng
// must be nonnil. A zero cap starts the same history as a fresh session owner.
// Equal jitter and retry budgets are local policy, not a vendor cooldown:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/state-timers.md#timers
func RetryBackoff(c Config, rng RNG, cap time.Duration) (delay, next time.Duration) {
	if cap == 0 {
		cap = firstBackoff(c)
	}
	lo := max(c.BackoffMin, cap/2+cap%2)
	delay = lo + time.Duration(rng.Uint64()%uint64(cap-lo+1))
	next = c.BackoffMax
	if cap <= c.BackoffMax/2 {
		next = min(c.BackoffMax, cap*2)
	}
	return delay, next
}

func (m *Machine) timer(out []Action) []Action {
	var d core.Instant
	switch m.state {
	case Dialing, Releasing, Backoff:
		d = m.until
		if m.lazyUntil != 0 {
			d = min(d, m.lazyUntil)
		}
	case Ready:
		d = m.idleUntil
	}
	if d != m.deadline {
		m.deadline = d
		k := ArmTimer
		if d == 0 {
			k = CancelTimer
		}
		a := m.action(k)
		a.Deadline = d
		out = append(out, a)
	}
	return out
}

// Step consumes one event synchronously. Regressing clocks are clamped. Stale
// attempt completions cannot mutate clients, timers or accounting. Local commands
// are owner inputs. ClientGone may omit Attempt only for lifetime-unique IDs.
func (m *Machine) Step(e Event, now core.Instant) (out []Action) {
	if now < m.last {
		m.discards++
	}
	now = max(now, m.last)
	m.last = now
	switch e.Kind {
	case Progress, Failed, Released, Usage:
		if e.Attempt != m.attempt {
			m.discards++
			return nil
		}
	case ClientGone:
		if e.Attempt != 0 && e.Attempt != m.attempt {
			m.discards++
			return nil
		}
	}
	if m.state == Closed {
		if e.Kind == Client {
			return []Action{m.clientAction(Reject, e.Client, ErrClosed)}
		}
		m.discards++
		return nil
	}
	// Demand expires independently of the worker's cleanup target or failure.
	// Process it before progress/release can admit clients or start a successor.
	var expired []Action
	if m.lazyUntil != 0 && now >= m.lazyUntil {
		expired = m.drop(ErrBusy)
		switch m.state {
		case Dialing:
			expired = append(expired, m.release(Dormant, ErrBusy, now)...)
		case Releasing:
			// Keep owning the worker and preserve every blocking/closed target.
			if m.after == Backoff && m.blockErr == nil {
				m.after = Dormant
			}
		case Backoff:
			m.state = Dormant
		}
		m.resetLazyOpen()
	}
	defer func() { out = m.timer(append(expired, out...)) }()
	switch e.Kind {
	case Start:
		if !m.started {
			m.started = true
			out = m.settle(now)
		} else {
			m.discards++
		}
	case Client:
		if !m.started {
			return []Action{m.clientAction(Reject, e.Client, ErrClosed)}
		}
		if m.state == Busy {
			if !m.lazyBusyEnabled() || m.blockErr != ErrBusy || !m.busyCloud {
				return []Action{m.clientAction(Reject, e.Client, m.blockErr)}
			}
			m.blockErr = nil
			m.busyCloud = false
			m.state = Dormant
		}
		if m.cfg.Idle == 0 && m.ownershipRecovery() && m.state != Releasing {
			return []Action{m.clientAction(Reject, e.Client, m.blockErr)}
		}
		// Queue lazy demand during refusal cleanup; Released owns replacement and backoff.
		if m.state == Releasing && !(m.lazyBusyEnabled() && m.blockErr == nil && (m.after == Dormant || (m.after == Backoff && m.refusalRetry))) {
			err := ErrTransport
			switch {
			case m.after == Closed:
				err = ErrClosed
			case m.blockErr != nil:
				err = m.blockErr
			}
			return []Action{m.clientAction(Reject, e.Client, err)}
		}
		if m.ambientRecovery() && (m.closeDuringRecovery || m.refusalRetry) && m.state != Ready {
			err := ErrTransport
			if m.refusalRetry {
				err = ErrBusy
			}
			return []Action{m.clientAction(Reject, e.Client, err)}
		}
		for _, c := range m.clients {
			if c.used && c.id == e.Client {
				m.discards++
				return nil
			}
		}
		if m.count == len(m.clients) || (m.state != Ready && m.queued >= m.cfg.MaxQueued) {
			return []Action{m.clientAction(Reject, e.Client, ErrBudget)}
		}
		for i := range m.clients {
			if !m.clients[i].used {
				m.clients[i] = clientSlot{e.Client, true, m.state == Ready}
				break
			}
		}
		m.count++
		m.idleUntil = 0
		if m.state == Ready {
			out = append(out, m.clientAction(Admit, e.Client, nil))
		} else {
			m.queued++
			if m.state == Dormant {
				out = m.settle(now)
			}
		}
	case ClientGone:
		found := false
		for i, c := range m.clients {
			if c.used && c.id == e.Client {
				found = true
				m.clients[i] = clientSlot{}
				m.count--
				if c.admitted {
					out = append(out, m.clientAction(Disconnect, c.id, nil))
				} else {
					m.queued--
				}
				break
			}
		}
		if !found {
			m.discards++
		}
		if m.count == 0 && m.lazyBusyEnabled() && (m.lazyUntil != 0 || m.state == Dialing) {
			m.resetLazyOpen()
			switch m.state {
			case Dialing:
				out = m.release(Dormant, ErrClosed, now)
			case Releasing:
				if m.after == Backoff {
					m.after = Dormant
				}
			case Backoff:
				m.state = Dormant
			}
		}
		if m.state == Ready && m.count == 0 && m.cfg.Idle > 0 && m.idleUntil == 0 {
			m.idleUntil = after(now, m.cfg.Idle)
		}
	case Progress:
		if m.state != Dialing || e.Layer != m.next {
			m.discards++
			break
		}
		m.next++
		if m.next == LayerCount {
			m.state = Ready
			m.closeDuringRecovery = true
			m.resetLazyOpen()
			m.retry = firstBackoff(m.cfg)
			for i, c := range m.clients {
				if c.used {
					m.clients[i].admitted = true
					out = append(out, m.clientAction(Admit, c.id, nil))
				}
			}
			m.queued = 0
			if m.count == 0 && m.cfg.Idle > 0 {
				m.idleUntil = after(now, m.cfg.Idle)
			}
		}
	case Failed:
		m.permanent = m.permanent || e.Permanent
		uncertain := errors.Is(e.Err, ErrStop) || errors.Is(e.Err, ErrUncertain) ||
			(errors.Is(e.Err, ErrBusy) && errors.Is(e.Err, ErrReleaseAbandoned))
		// A refused canceled/expired generation cannot block demand queued
		// during its cleanup. Ownership uncertainty still wins below.
		retiredDemand := m.after == Dormant && (m.state == Releasing || m.state == Dormant || m.state == Backoff)
		if m.lazyBusyEnabled() && retiredDemand && m.lazyUntil == 0 && e.Layer == Cloud && errors.Is(e.Err, ErrBusy) && !uncertain {
			// Retirement still depends on the final cleanup being clean.
			m.releaseRefusal = true
			return nil
		}
		lazyRefusal := m.lazyBusyEnabled() && m.state == Dialing && e.Layer == Cloud && errors.Is(e.Err, ErrBusy) && !uncertain && m.blockErr == nil
		ambientRefusal := m.ambientRecovery() && e.Layer == Cloud && errors.Is(e.Err, ErrBusy) && !uncertain && m.blockErr == nil
		recentRefusal := ambientRefusal || lazyRefusal || (!m.lazyBusyEnabled() && errors.Is(e.Err, ErrBusy) && m.releasedAttempt != 0 && m.releasedAttempt < e.Attempt && now < m.recentUntil && m.blockErr == nil)
		if recentRefusal {
			m.refusalRetry = true
		} else if (m.lazyBusyEnabled() || m.ambientRecovery()) && m.state == Dialing && !uncertain && !errors.Is(e.Err, ErrBusy) {
			// This dial is transport recovery, not refusal cleanup. The shared
			// drop below retires its demand budget without erasing retry history.
			m.refusalRetry = false
		}
		blocked := uncertain || (errors.Is(e.Err, ErrBusy) && !recentRefusal)
		if m.state != Dialing && m.state != Ready && m.state != Releasing && !blocked {
			m.discards++
		}
		if blocked && m.attempt != 0 {
			// A terminal answer for this generation wins even if cleanup or a
			// timeout was processed first. Uncertainty takes priority over busy.
			if uncertain {
				m.blockErr = ErrUncertain
				if errors.Is(e.Err, ErrStop) {
					m.blockErr = ErrStop
				}
			} else if m.blockErr != ErrStop && m.blockErr != ErrUncertain {
				m.blockErr = ErrBusy
				m.busyCloud = e.Layer == Cloud
			}
			switch m.state {
			case Releasing:
				if m.after != Closed {
					m.after = Busy
				}
			case Dormant, Backoff, Busy:
				m.state = Busy
				out = m.drop(m.blockErr)
				out = append(out, m.settle(now)...)
			}
		}
		if m.state == Dialing || m.state == Ready {
			target, err := Backoff, ErrTransport
			if recentRefusal {
				err = ErrBusy
			}
			if uncertain {
				target, err = Busy, m.blockErr
			} else if errors.Is(e.Err, ErrBusy) && !recentRefusal {
				target, err = Busy, ErrBusy
				m.blockErr = ErrBusy
			}
			out = m.release(target, err, now)
		}
	case Released:
		if m.state != Releasing {
			m.discards++
			break
		}
		if e.Err == nil && e.ReleasedAllocation {
			m.recentUntil = after(now, m.cfg.RecentReleaseWindow)
			m.releasedAttempt = e.Attempt
		}
		m.usage = [LayerCount]Limit{}
		if errors.Is(e.Err, ErrReleaseAbandoned) {
			if m.releaseRefusal {
				// Lazy refusal permission requires confirmed cleanup. Transport
				// recovery retains its separate abandonment authorization.
				m.blockErr = ErrStop
				if m.after != Closed {
					m.after = Busy
				}
			} else {
				m.retryPending = true
			}
		}
		if m.retryPending {
			m.backoff(now)
		}
		m.state = m.after
		if e.Err != nil && m.state == Closed {
			a := m.action(Stopped)
			a.Err = ErrStop
			return []Action{a}
		}
		if e.Err != nil && !errors.Is(e.Err, ErrReleaseAbandoned) && m.state != Closed {
			m.state = Busy
			m.blockErr = ErrStop
		}
		if m.state == Busy {
			out = m.drop(m.blockErr)
		}
		out = append(out, m.settle(now)...)
	case Usage:
		if m.state != Dialing && m.state != Ready {
			m.discards++
			break
		}
		if e.Layer >= LayerCount {
			m.discards++
			break
		}
		l := m.cfg.Budgets.Layers[e.Layer]
		if e.Bytes < 0 || e.Count < 0 || e.Bytes > l.Bytes || e.Count > l.Count {
			out = m.release(Busy, ErrBudget, now)
			m.blockErr = ErrBudget
		} else {
			m.usage[e.Layer] = Limit{e.Bytes, e.Count}
		}
	case Shutdown:
		out = m.release(Closed, ErrClosed, now)
	case Tick:
		switch m.state {
		case Dialing:
			if now >= m.until {
				out = m.release(Backoff, ErrTimeout, now)
			}
		case Releasing:
			if now >= m.until {
				// The stop completion can race this deadline. Keep ownership until
				// the worker joins; only an adapter violating its bounded contract
				// is fatal after an additional DialTimeout grace period.
				if m.after != Closed && !m.releaseOverdue {
					m.releaseOverdue = true
					m.until = after(now, m.cfg.DialTimeout)
					break
				}
				m.state = Closed
				out = append(m.drop(ErrTimeout), Action{Kind: Stopped, Attempt: m.attempt, Err: ErrTimeout})
			}
		case Backoff:
			if now >= m.until {
				if m.cfg.Idle > 0 && m.count == 0 {
					m.state = Dormant
				} else {
					out = m.dial(now)
				}
			}
		case Ready:
			if m.idleUntil != 0 && now >= m.idleUntil {
				out = m.release(Dormant, nil, now)
			}
		}
	}
	return out
}
