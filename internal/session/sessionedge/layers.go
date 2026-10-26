// Package sessionedge runs the session owner and composes bounded layer adapters.
// It performs no vendor protocol work; adapters provide each protocol layer.
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/state-timers.md
package sessionedge

import (
	"context"
	"errors"

	"github.com/zmerlynn/seestar-wormhole/internal/core"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// ErrPanic is a sanitized edge panic; panic values may contain credentials.
var ErrPanic = errors.New("session edge panic")

// ErrInputClosed means the owner command stream ended.
var ErrInputClosed = errors.New("session input closed")

// ErrAdapter means a layer violated its integration contract.
var ErrAdapter = errors.New("invalid session adapter")

// Lease owns an allocation. Stop must honor its independent context, even after
// attempt cancellation. The worker invokes it exactly once, including failed dial.
// Implementations may wrap cloudhttp.Lease.Close, which already bounds cloud stop.
// ErrReleaseAbandoned opts into fresh-allocation recovery after that bound;
// other stop errors delay replacement after cleanup joins. Final failed stop
// is always surfaced.
// Stop is distinct from local transport teardown; replacement ownership is local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#reports-and-stop
type Lease interface{ Stop(context.Context) error }

// Allocator returns an owned lease even on a partially successful allocation.
// It must honor cancellation and clean up any allocation not handed back (including
// panic or ambiguous response parsing). Map documented allocation refusals to
// session.ErrBusy. Internal stop failures (including wrapped/joined cloudhttp
// cleanup errors) add session.ErrReleaseAbandoned after bounded stop/local teardown.
// A received internal stop denial or malformed reply also adds session.ErrStop.
// Join every observed outcome monotonically: fatal/denied > malformed >
// owed-release > retryable transport; secondary errors cannot lower disposition.
// Local cleanup failure retains session.ErrStop. Permanent acquisition failures
// set diagnostic.Terminal.Permanent before publication, independently of stop policy.
// Malformed or unknown allocation
// responses map to session.ErrUncertain. Network errors can request fresh
// online-link; uncertainty is not retryable. A returned partial lease
// transfers its one Stop obligation to the worker; never also stop it internally.
// An allocation panic without a handed-back lease is conservatively uncertain.
// No retries may be hidden inside an allocation adapter.
type Allocator interface {
	Allocate(context.Context) (Lease, error)
}

// Transport owns one layer's local resources. Close must synchronously cancel
// its workers and release local resources without waiting on a peer. After Close
// returns, notification producers must be stopped and queued terminal outcomes
// must remain readable (including when Close returns an error). Events is a
// nonblocking accessor returning the same channel before and after Close. It must
// be a bounded channel carrying only Failed or absolute Usage reports, excluding
// channel reservations. Closed Events is a transport failure. Nil disables it.
// Construction methods must honor cancellation and return partial resources on
// error, with their terminal outcomes in Events even when cancellation won;
// work/queues/copies must fit the supplied per-layer budget.
type Transport interface {
	Close() error
	Events() <-chan session.Event
}

// Rendezvous constructs a fresh IOTC attempt from the ephemeral allocation.
type Rendezvous interface {
	Dial(context.Context, Lease) (Transport, error)
}

// Handshaker authenticates the datagram path; it must never fall back to plaintext.
type Handshaker interface {
	Handshake(context.Context, Transport) (Transport, error)
}

// Reliable creates a packet-preserving reliable transport after authentication.
type Reliable interface {
	Create(context.Context, Transport) (Transport, error)
}

// Tunneler starts tunnel login on a ready reliable transport.
type Tunneler interface {
	Start(context.Context, Transport) (Channels, error)
}

// Channels owns device-side client channels. TryOpen/TryClose must be nonblocking
// bounded submissions. TryOpen reserves at most the configured Channel budget.
// Failure to open a single channel rejects only that client. Close cancels all
// clients, including blocked writers, without waiting on their output.
type Channels interface {
	Transport
	TryOpen(client uint64) error
	TryClose(client uint64)
}

// Layers is the per-attempt composition; none of its state may survive redial.
type Layers struct {
	Cloud      Allocator
	Rendezvous Rendezvous
	DTLS       Handshaker
	RDT        Reliable
	Tunnel     Tunneler
}

// Factory creates fresh layer instances under the supplied reservations. New
// must be bounded and must not allocate a device session itself. The worker calls
// it once per generation, with a fresh, independently cancellable context.
type Factory interface {
	New(attempt uint64, budgets session.Budgets) (Layers, error)
}

// Driver supplies bounded edge completions to the same goroutine that owns the
// session machine. Callbacks must not block or do socket I/O; they drive pure
// layer machines with the supplied monotonic time. The factory owns the queue,
// accounts its storage in the inbox reservation, and never closes it during Run.
// Attempt cancellation must join workers without requiring another callback.
type Driver interface {
	Work() <-chan func(core.Instant)
}

// Sink delivers client outcomes without blocking the owner; errors terminate Run.
// It must enforce its own bounded output within the configured inbox reservation.
// Run calls it only for Admit, Reject, Disconnect and Stopped.
type Sink interface{ Try(session.Action) error }

// ChannelSink is a bounded nonblocking notification queue. The consumer owns the
// channel and must keep it open until Run returns; queued metadata is part of
// its configured resource reservation. A full queue terminates Run with cleanup.
type ChannelSink chan session.Action

// Try enqueues one outcome without waiting for a slow client or output consumer.
func (s ChannelSink) Try(a session.Action) error {
	select {
	case s <- a:
		return nil
	default:
		return session.ErrBudget
	}
}
