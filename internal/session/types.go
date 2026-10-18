package session

import (
	"errors"
	"math"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// ErrBusy is an allocation refusal; the owner applies local refusal-retry policy.
var ErrBusy = errors.New("device in use by another session")

// ErrBudget means a bounded queue or byte/count reservation cannot fit.
var ErrBudget = errors.New("session budget exceeded")

// ErrClosed means the owner has shut down.
var ErrClosed = errors.New("session closed")

// ErrTransport means an attempt failed; layer diagnostics belong at the edge.
var ErrTransport = errors.New("session transport failed")

// ErrTimeout means a dial or local release deadline expired.
var ErrTimeout = errors.New("session deadline exceeded")

// ErrUncertain means allocation ownership could not be determined. No automatic
// retry is safe; stop the owner and resolve remote ownership before restarting.
var ErrUncertain = errors.New("session allocation outcome uncertain")

// ErrStop means release could not be confirmed; replacement remains blocked.
var ErrStop = errors.New("session release failed")

// ErrReleaseAbandoned means a bounded cloud stop failed after local teardown.
// Recovery may obtain fresh online-link credentials; remote release is unconfirmed.
// Allocation cleanup can leave remote ownership uncertain after local teardown.
// Fresh allocation after abandoned stop is local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/state-timers.md#app-workflow
// Allocation expiry and reuse remain unresolved (R14):
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#allocation-online-link
var ErrReleaseAbandoned = errors.New("session cloud release abandoned")

// State is the session owner's lifecycle.
type State uint8

const (
	Dormant State = iota // Lazy mode waiting for demand, or not started.
	Dialing
	Ready
	Releasing
	Backoff
	Busy
	Closed State = 7 // Preserve the existing noncontrol trace tag.
)

// Layer is the ordered attempt pipeline. These are internal tags, not wire IDs.
type Layer uint8

const (
	Cloud Layer = iota
	Rendezvous
	DTLS
	RDT
	Tunnel
	LayerCount
)

// Kind distinguishes commands and typed, attempt-stamped completions.
type Kind uint8

const (
	Start Kind = iota
	Client
	ClientGone
	Progress // Layer completed successfully, in pipeline order.
	Failed
	Released // All workers joined, transports closed and cloud stop attempted.
	Usage    // Absolute retained byte/count usage for one layer.
	Tick
	Shutdown Kind = 10 // Preserve the existing noncontrol trace tag.
)

// Event carries no wire payload or secret. Client IDs must be unique while live.
// ClientGone with Attempt zero is a local cancellation command and requires an
// ID unique for the owner lifetime, allowing cancellation before admission.
// Progress/Failed/Released/Usage require Attempt. Edges retain allocation ownership
// themselves; a discarded stale completion can never discard a lease.
type Event struct {
	Kind               Kind
	Attempt            uint64
	Client             uint64
	Layer              Layer
	Bytes, Count       int
	Err                error
	ReleasedAllocation bool // Released only: this worker actually stopped an owned lease.
}

// Effect describes bounded, nonblocking work for the edge.
type Effect uint8

const (
	Dial Effect = iota
	Release
	Admit
	Reject
	Disconnect
	ArmTimer
	CancelTimer
	Stopped
)

// Action carries its originating attempt, including client and timer effects.
// Reject/Disconnect errors are sanitized package sentinels.
type Action struct {
	Kind            Effect
	Attempt, Client uint64
	Deadline        core.Instant
	Err             error
}

// Limit bounds both retained bytes and object count. Zero permits no usage.
type Limit struct{ Bytes, Count int }

// Budgets reserves all layer maxima, inbox storage and MaxClients channel
// reservations in one aggregate. Layer reports exclude the separately reserved
// channel storage; adapter copies must be included in a layer or inbox maximum.
type Budgets struct {
	Layers                [LayerCount]Limit
	Inbox, Channel, Total Limit
}

// OwnSessionRetryWindow is the local refusal-retry horizon after our session ends.
// Post-release busy responses can persist for 35–45s; 90s leaves margin without
// inventing a vendor cooldown or retrying forever.
const OwnSessionRetryWindow = 90 * time.Second

// LazyBusyWindow bounds client waiting from the first client-triggered dial,
// independently of release cleanup. Occupied prechecks can persist beyond 22s,
// with detail passing by 32s and readiness at 36s after orderly release. Handover
// after an unclean exit can take 35–81s. Keep 90s for margin; this local policy
// is not a vendor timer.
const LazyBusyWindow = 90 * time.Second

// Config contains local policy, not protocol constants. Idle == 0 is eager mode.
// By default eager busy retries within the own-session-end window. LazyBusyWindow
// additionally enables bounded demand-driven lazy cloud refusal retries.
// RetryBusy lets an ambient owner retry eager cloud refusals indefinitely.
type Config struct {
	Idle, DialTimeout, StopTimeout time.Duration
	RecentReleaseWindow            time.Duration // Local policy; zero disables refusal retries.
	LazyBusyWindow                 time.Duration // Client-triggered busy budget; zero retains the own-session retry policy.
	RetryBusy                      bool          // Eager cloud refusal retries, including first acquisition.
	// SingleBusyWindow retains one LazyBusyWindow deadline from the first dial
	// across initial demand/retries and all occupancy waits. After Ready, transport
	// recovery may outlive it; a busy answer cannot renew it. With a zero window,
	// busy always fails fast, including after release.
	SingleBusyWindow       bool
	BackoffMin, BackoffMax time.Duration
	MaxClients, MaxQueued  int
	Budgets                Budgets
}

// DefaultConfig returns bounded local policy: 30s dial, 5s stop, 0.5–30s retry.
// Deadlines, retry budgets and backoff caps are local choices within inferred guidance:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/state-timers.md#timers
func DefaultConfig() Config {
	c := Config{RecentReleaseWindow: OwnSessionRetryWindow, DialTimeout: 30 * time.Second, StopTimeout: 5 * time.Second,
		BackoffMin: 500 * time.Millisecond, BackoffMax: 30 * time.Second, MaxClients: 256, MaxQueued: 16}
	for i := range c.Budgets.Layers {
		c.Budgets.Layers[i] = Limit{8 << 20, 1024}
	}
	c.Budgets.Inbox = Limit{1 << 20, 1024}
	c.Budgets.Channel = Limit{256 << 10, 4}
	c.Budgets.Total = Limit{105 << 20, 7168}
	return c
}

// RNG supplies deterministic retry jitter; never used for protocol cryptography.
type RNG interface{ Uint64() uint64 }

func addLimit(a, b Limit) (Limit, bool) {
	if b.Bytes < 0 || b.Count < 0 || a.Bytes > math.MaxInt-b.Bytes || a.Count > math.MaxInt-b.Count {
		return Limit{}, false
	}
	return Limit{a.Bytes + b.Bytes, a.Count + b.Count}, true
}

// Reservation sums all configured maxima with checked arithmetic.
func (c Config) Reservation() (Limit, error) {
	total := Limit{}
	items := append([]Limit{c.Budgets.Inbox}, c.Budgets.Layers[:]...)
	if c.MaxClients < 1 || c.MaxClients > 65536 {
		return total, ErrBudget
	}
	for _, l := range items {
		var ok bool
		total, ok = addLimit(total, l)
		if !ok {
			return Limit{}, ErrBudget
		}
	}
	l := c.Budgets.Channel
	if l.Bytes < 0 || l.Count < 0 || l.Bytes > math.MaxInt/c.MaxClients || l.Count > math.MaxInt/c.MaxClients {
		return Limit{}, ErrBudget
	}
	var ok bool
	total, ok = addLimit(total, Limit{l.Bytes * c.MaxClients, l.Count * c.MaxClients})
	if !ok || total.Bytes > c.Budgets.Total.Bytes || total.Count > c.Budgets.Total.Count {
		return Limit{}, ErrBudget
	}
	return total, nil
}
