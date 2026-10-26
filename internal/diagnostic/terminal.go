package diagnostic

import (
	"errors"
	"sync/atomic"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// StackLayer attributes a composed runtime failure, never a usage or progress
// report. Failed events do not index protocol layer budgets.
const StackLayer session.Layer = session.LayerCount

// Terminal carries bounded diagnostic attribution independently of policy.
// Copies made while classifying policy share the once-only reporting receipt.
type Terminal struct {
	origin    session.Layer
	cause     uint8
	priority  uint8 // The receipt's own blocking disposition, before any join.
	rank      uint8 // Informative received cause (2), obligated cancellation (1), quiet (0).
	policy    error
	reported  *atomic.Bool
	next      *Terminal // Immutable received receipts from this one normalized outcome.
	Permanent bool      // Acquisition barrier; set before publication, independent of attribution.
}

var terminalCauses = [...]string{
	"transport_failed", "timeout", "capacity", "login_rejected",
	"authentication_unsupported", "invalid_frame", "peer_closed", "dtls_required",
	"canceled", "inbox_capacity", "step_budget", "packet_delivery_failed",
	"send_capacity", "local_queue_capacity", "read_failed", "write_failed",
	"handshake_failed", "invalid_license", "invalid_identifier", "busy",
	"release_failed", "ownership_uncertain", "liveness_timeout", "connection_timeout",
	"server_refused", "operation_failed", "unauthorized",
	"device_offline", "invalid_response", "release_abandoned",
	"rejected", "reauthentication_required", "invalid_configuration",
	"cloud_license_missing",
	"inconsistent server answer: detail identity differs from requested pair",
}

// NewTerminal discards unrecognized labels and retains only session policy
// sentinels. No dependency error strings or action envelopes cross this boundary.
func NewTerminal(origin session.Layer, reason string, policy error) *Terminal {
	if origin > StackLayer {
		origin = StackLayer
	}
	var cause uint8
	for i, label := range terminalCauses {
		if reason == label {
			cause = uint8(i)
			break
		}
	}
	clean := Disposition(policy)
	if clean == session.ErrReleaseAbandoned {
		clean = errors.Join(session.ErrTransport, clean)
	}
	rank := uint8(2)
	if reason == "canceled" {
		rank = 0
		if errors.Is(clean, session.ErrStop) || errors.Is(clean, session.ErrUncertain) || errors.Is(clean, session.ErrBusy) || errors.Is(clean, session.ErrReleaseAbandoned) {
			rank = 1
		}
	}
	return &Terminal{origin: origin, cause: cause, rank: rank, priority: blockingPriority(clean), policy: clean, reported: new(atomic.Bool)}
}

func (e *Terminal) Error() string { return e.Phase() + ": " + terminalCauses[e.cause] }
func (e *Terminal) Unwrap() error { return e.policy }

// Origin returns the actual failing layer, regardless of the notification queue.
func (e *Terminal) Origin() session.Layer { return e.origin }

// Phase returns a fixed layer name.
func (e *Terminal) Phase() string {
	return [...]string{"cloud", "rendezvous", "dtls", "rdt", "tunnel", "stack"}[e.origin]
}

// SharesReceipt reports whether two bounded terminals describe the same report.
func (e *Terminal) SharesReceipt(other *Terminal) bool {
	return e != nil && other != nil && e.reported != nil && e.reported == other.reported
}

// Disposition is the single session policy rule: stop > uncertain > busy >
// transport. Owed release is orthogonal and survives every priority selection.
func Disposition(err error) error {
	clean := []error{session.ErrTransport, session.ErrBusy, session.ErrUncertain, session.ErrStop}[blockingPriority(err)]
	if errors.Is(err, session.ErrReleaseAbandoned) {
		if clean == session.ErrTransport {
			return session.ErrReleaseAbandoned
		}
		clean = errors.Join(clean, session.ErrReleaseAbandoned)
	}
	return clean
}

func blockingPriority(err error) uint8 {
	for i, sentinel := range []error{session.ErrStop, session.ErrUncertain, session.ErrBusy} {
		if errors.Is(err, sentinel) {
			return uint8(3 - i)
		}
	}
	return 0
}

func terminalReason(t *Terminal) string {
	reason := terminalCauses[t.cause]
	if reason == "canceled" {
		switch {
		case errors.Is(t.policy, session.ErrStop):
			return "release_failed"
		case errors.Is(t.policy, session.ErrUncertain):
			return "ownership_uncertain"
		case errors.Is(t.policy, session.ErrBusy):
			return "busy"
		case errors.Is(t.policy, session.ErrReleaseAbandoned):
			return "release_abandoned"
		}
	}
	return reason
}

func phaseLayer(phase string) (session.Layer, bool) {
	for layer, name := range [...]string{"cloud", "rendezvous", "dtls", "rdt", "tunnel", "stack"} {
		if phase == name {
			return session.Layer(layer), true
		}
	}
	return 0, false
}

// walkReceived streams receipts without storing wrapper histories or allocating
// a batch. An attachment is a received leaf only when its cause has no receipts.
// Context around either representation must not replace independent descendants.
func walkReceived(err error, typed func(*Terminal), attached func(*failure)) {
	if t, ok := err.(*Terminal); ok {
		for ; t != nil; t = t.next {
			typed(t)
		}
		return
	}
	if f, ok := err.(*failure); ok && !HasReceipt(f.err) {
		attached(f)
		return
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			walkReceived(cause, typed, attached)
		}
	} else if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		walkReceived(wrapped.Unwrap(), typed, attached)
	}
}

// receivedCause compares receipt kind and its own blocking priority, never
// the projected aggregate policy. Ties are stable in receive/join order.
// Informative causes outrank obligated cancellation; origins belong to receipts.
func receivedCause(err error) (*Terminal, *failure) {
	var received *Terminal
	var attachment *failure
	var rank, priority uint8
	prefer := func(nextRank, nextPriority uint8) bool {
		return nextRank > rank || (nextRank == rank && nextPriority > priority)
	}
	walkReceived(err, func(t *Terminal) {
		if prefer(t.rank, t.priority) {
			received, attachment = t, nil
			rank, priority = t.rank, t.priority
		}
	}, func(f *failure) {
		ownPriority := blockingPriority(f.err)
		ownRank := uint8(2)
		if f.reason == "canceled" {
			ownRank = 0
			if ownPriority > 0 || errors.Is(f.err, session.ErrReleaseAbandoned) {
				ownRank = 1
			}
		}
		if prefer(ownRank, ownPriority) {
			received, attachment = nil, f
			rank, priority = ownRank, ownPriority
		}
	})
	return received, attachment
}

// RetainTerminal applies the composition rule to one incoming outcome. Every
// distinct received receipt survives; only shared receipt identity deduplicates.
// Its compact immutable chain contains bounded labels and policy, never raw
// errors. The chain replaces the incoming diagnostic graph; it is not an owner
// history or a queue batch. Owner aggregation must use Primary after observation.
func RetainTerminal(err, policy error) error { return retainTerminal(err, policy, nil) }

// AtLayer normalizes an adapter outcome, using its actual layer only when the
// selected cause has no registered attached origin or existing typed origin.
func AtLayer(err error, layer session.Layer, policy error) error {
	if err == nil {
		return NewTerminal(layer, "transport_failed", policy)
	}
	return retainTerminal(err, policy, &layer)
}

func retainTerminal(err, policy error, fallback *session.Layer) error {
	if err == nil {
		return policy
	}
	policy = Disposition(errors.Join(err, policy))
	phase, reason, selected := selectedCause(err, "")
	origin, registered := phaseLayer(phase)
	if selected != nil {
		origin, registered = selected.origin, true
	}
	if selected == nil && fallback != nil {
		origin, registered = *fallback, true
	}
	if !registered {
		if !HasReceipt(err) {
			return policy
		}
		origin = StackLayer
	}
	head := NewTerminal(origin, reason, policy)
	if selected != nil {
		head.reported = selected.reported
		head.rank = selected.rank
		head.priority = selected.priority
	} else if _, attached := receivedCause(err); attached != nil {
		head.reported = attached.reported
		head.priority = blockingPriority(attached.err)
		if attached.reason == "canceled" {
			head.rank = 1
		}
	} else if Class(err) == "canceled" {
		// Quiet attachments do not compete with informative receipts, but a
		// later policy translation must still share the original identity.
		// Use the same leaf walk so an operation wrapper cannot lend its receipt.
		var original *failure
		walkReceived(err, func(*Terminal) {}, func(f *failure) {
			if original == nil && f.reason == "canceled" {
				original = f
			}
		})
		if original != nil {
			head.reported = original.reported
		}
	}
	// A policy translation can reveal an obligation on an otherwise quiet
	// received cancellation. Canonicalize it on that same receipt, once, before
	// later joins project an aggregate policy onto it.
	if head.rank == 0 && terminalReason(head) != "canceled" {
		own := NewTerminal(head.origin, terminalReason(head), head.policy)
		own.reported = head.reported
		own.rank = 1
		head = own
	}
	tail := head
	appendReceipt := func(t *Terminal) {
		if terminalReason(t) == "canceled" {
			return
		}
		for prior := head; prior != nil; prior = prior.next {
			if prior.SharesReceipt(t) {
				return
			}
		}
		next := NewTerminal(t.origin, terminalReason(t), t.policy)
		next.reported = t.reported
		next.rank = t.rank
		next.priority = t.priority
		next.Permanent = t.Permanent
		tail.next = next
		tail = next
	}
	walkReceived(err, func(t *Terminal) {
		head.Permanent = head.Permanent || t.Permanent
		appendReceipt(t)
	}, func(f *failure) {
		origin, ok := phaseLayer(f.phase)
		if !ok {
			origin = StackLayer
			if fallback != nil {
				origin = *fallback
			}
		}
		t := NewTerminal(origin, f.reason, Disposition(f.err))
		t.reported = f.reported
		appendReceipt(t)
	})
	return head
}

// JoinTerminals combines policy and received diagnostics at every edge join.
// An informative receipt outranks an obligated cancellation; among informative
// receipts the strongest own blocking disposition wins, then receive/join order.
// A projected aggregate policy never changes a receipt's own priority;
// blocking policy is selected independently and owed release survives either
// order. Neither policy precedence nor cancellation can borrow an origin.
func JoinTerminals(a, b error) error {
	joined := errors.Join(a, b)
	return RetainTerminal(joined, Disposition(joined))
}

// Primary keeps only the policy primary after all received receipts have reached
// the observer. It shares the selected receipt and cannot accumulate history.
// Cancellation without an independent failure or owed release has no primary.
func Primary(err error) error {
	var t *Terminal
	if !errors.As(err, &t) {
		if errors.Is(err, session.ErrStop) || errors.Is(err, session.ErrUncertain) || errors.Is(err, session.ErrBusy) || errors.Is(err, session.ErrReleaseAbandoned) {
			return err
		}
		return nil
	}
	if terminalReason(t) == "canceled" {
		return nil
	}
	copy := *t
	copy.next = nil
	return &copy
}

// reportReceived is shared by owner and final reporting. Normalization and
// reporting use the same composition rule and receipt identity, including an
// owed-release cancellation whose label changes during policy projection.
func reportReceived(err error, phase string, emit func(string, string)) bool {
	// Stream the same received leaves used by normalization. Direct final
	// reporting also accepts trusted operation attachments (such as release)
	// outside the protocol-layer vocabulary without borrowing another origin.
	handled, emitted := false, false
	report := func(phase, reason string, receipt *atomic.Bool) {
		if reason == "canceled" {
			return
		}
		handled = true
		if receipt != nil && receipt.Swap(true) {
			return
		}
		emit(phase, reason)
		emitted = true
	}
	walkReceived(err, func(t *Terminal) { report(t.Phase(), terminalReason(t), t.reported) }, func(f *failure) {
		reason := terminalReason(NewTerminal(StackLayer, f.reason, Disposition(f.err)))
		// Non-cancellation operation labels are trusted attachments, including
		// those outside the narrower session terminal vocabulary.
		if f.reason != "canceled" {
			reason = f.reason
		}
		report(f.phase, reason, f.reported)
	})
	if handled {
		return emitted
	}
	phase, reason, _ := selectedCause(err, phase)
	if reason == "canceled" && onlyCancellationCauses(err) {
		return false
	}
	emit(phase, reason)
	return true
}

// EachTerminal streams standalone normalized receipts, so observers can retain
// one policy primary while reporting independent siblings without a batch copy.
func EachTerminal(err error, visit func(*Terminal)) {
	var t *Terminal
	if !errors.As(err, &t) {
		return
	}
	for ; t != nil; t = t.next {
		copy := *t
		copy.next = nil
		visit(&copy)
	}
}

// HasReceipt distinguishes independently received diagnostics from legacy
// untyped constructor/queue echoes. Matching labels never establish identity.
func HasReceipt(err error) bool {
	var terminal *Terminal
	var attached *failure
	return errors.As(err, &terminal) || errors.As(err, &attached)
}
