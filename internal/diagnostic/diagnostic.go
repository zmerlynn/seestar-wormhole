// Package diagnostic classifies failures without formatting dependency errors.
package diagnostic

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/oserror"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"syscall"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

const licenseMissingHint = "the cloud allocation carried no SDK license key, so the connection cannot be set up; this is a service-side change, not your configuration; report the failure"

// OptionError identifies a configuration setting using only a registered name
// and a fixed validation rule. Supplied values and dependency errors must never
// be stored in it.
type OptionError struct{ Option, Rule string }

func (e *OptionError) Error() string { return e.Option + ": " + e.Rule }

type failure struct {
	phase, reason string
	err           error
	reported      *atomic.Bool
}

func (e *failure) Error() string { return e.phase + ": " + e.reason }
func (e *failure) Unwrap() error { return e.err }

// Wrap attaches trusted operation/reason labels, never raw input values.
// Reasons are constants.
// An empty reason selects the fixed error vocabulary.
func Wrap(phase, reason string, err error) error {
	if err == nil {
		return nil
	}
	if reason == "" {
		reason = Class(err)
	}
	return &failure{phase: phase, reason: reason, err: err, reported: new(atomic.Bool)}
}

// Fields returns the most specific attached operation, or a sanitized fallback.
func Fields(err error, phase string) (string, string) {
	phase, reason, _ := selectedCause(err, phase)
	return phase, reason
}

// selectedCause uses the complete outcome for attribution; callers project
// policy separately and consume only the selected received terminal's receipt.
func selectedCause(err error, phase string) (string, string, *Terminal) {
	if errors.Is(err, ErrPullPanic) {
		return phase, "operation_panic", nil
	}
	terminal, attached := receivedCause(err)
	if terminal != nil {
		return terminal.Phase(), terminalReason(terminal), terminal
	}
	if attached != nil {
		if origin, ok := phaseLayer(attached.phase); ok {
			t := NewTerminal(origin, attached.reason, Disposition(attached.err))
			t.reported = attached.reported
			return attached.phase, terminalReason(t), t
		}
		reason := attached.reason
		if reason == "canceled" {
			reason = terminalReason(NewTerminal(StackLayer, reason, Disposition(attached.err)))
		}
		return attached.phase, reason, nil
	}
	var f *failure
	fallbackPhase := phase
	if errors.As(err, &terminal) {
		phase = terminal.Phase()
	} else if errors.As(err, &f) {
		phase = f.phase
	}
	reason := Class(err)
	if onlyCancellationCauses(err) && (terminal != nil || (f != nil && f.reason == "canceled") || reason == "canceled") {
		return phase, "canceled", terminal
	}
	if reason == "canceled" || (terminal != nil && reason == "transport_failed") {
		reason = "operation_failed"
	}
	if terminal != nil {
		phase = fallbackPhase
	}
	return phase, reason, nil
}

// Class never calls Error, String or LogValue on an untrusted value.
func Class(err error) string {
	if errors.Is(err, ErrPullPanic) {
		return "operation_panic"
	}
	// A complete cloud reply is a received cause even when release I/O or
	// cancellation also appears in its outcome. This precedence used to live
	// only in the cloud adapter; normalization now applies it at every boundary.
	for _, known := range []struct {
		cause  error
		reason string
	}{
		{cloud.ErrUnauthorized, "unauthorized"}, {cloud.ErrReauthentication, "reauthentication_required"},
		{cloud.ErrBusy, "busy"}, {cloud.ErrOffline, "device_offline"}, {cloud.ErrRejected, "rejected"},
		{cloud.ErrLicenseMissing, "cloud_license_missing"},
		{cloud.ErrInvalid, "invalid_response"}, {cloud.ErrConfig, "invalid_configuration"},
		{cloud.ErrInternal, "operation_failed"},
	} {
		if errors.Is(err, known.cause) {
			return known.reason
		}
	}

	if reason := typedPullClass(err); reason != "" {
		return reason
	}

	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsNotFound {
		return "dns_not_found"
	}
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, session.ErrTimeout):
		return "timeout"
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	case oserror.Is(err, syscall.ENOTDIR):
		return "not_a_directory"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case oserror.Is(err, syscall.EADDRINUSE):
		return "address_in_use"
	case oserror.Is(err, syscall.EADDRNOTAVAIL):
		return "address_unavailable"
	case oserror.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case oserror.Is(err, syscall.EPIPE):
		return "broken_pipe"
	case oserror.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case oserror.Is(err, syscall.ELOOP):
		return "symlink_rejected"
	case oserror.Is(err, syscall.EMFILE), oserror.Is(err, syscall.ENFILE):
		return "descriptor_limit"
	case oserror.Is(err, syscall.ENOSPC):
		return "no_space"
	case oserror.Is(err, syscall.EROFS):
		return "read_only"
	case errors.Is(err, session.ErrBusy):
		return "busy"
	case errors.Is(err, session.ErrBudget):
		return "capacity"
	case errors.Is(err, session.ErrStop):
		return "release_failed"
	case errors.Is(err, session.ErrUncertain):
		return "ownership_uncertain"
	case errors.Is(err, session.ErrReleaseAbandoned):
		return "release_abandoned"
	// A known received cause survives a joined cancellation. Generic transport
	// wrappers alone do not establish an independent failure during shutdown.
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, cloud.ErrTransport), errors.Is(err, session.ErrTransport):
		return "transport_failed"
	}
	return "operation_failed"
}

// Log emits the classified operation and cause, with fixed guidance when available.
func Log(l *slog.Logger, message, phase string, err error) {
	if l == nil || err == nil {
		return
	}
	reportReceived(err, phase, func(phase, reason string) {
		attrs := []any{"phase", phase, "reason", reason}
		if reason == "cloud_license_missing" {
			attrs = append(attrs, "hint", licenseMissingHint)
		}
		l.Error(message, attrs...)
	})
}

// CleanupError separates actual release/local cleanup failure from a retained
// owner outcome. Session.Close retains both; only ErrStop denotes failed cleanup.
func CleanupError(err error) error {
	if errors.Is(err, session.ErrStop) {
		return session.ErrStop
	}
	return nil
}

// CleanupClass identifies final release status independently of a retained
// primary and cancellation joined into the owner's final outcome.
func CleanupClass(err error) string {
	return Class(CleanupError(err))
}

// onlyCancellationCauses checks the complete outcome, including siblings of a
// typed terminal. Generic transport markers can accompany canceled I/O; other
// leaves establish an independent failure and must keep their final report.
func onlyCancellationCauses(err error) bool {
	if terminal, ok := err.(*Terminal); ok {
		return terminal.next == nil && terminalCauses[terminal.cause] == "canceled" &&
			!errors.Is(terminal, session.ErrStop) && !errors.Is(terminal, session.ErrUncertain) &&
			!errors.Is(terminal, session.ErrBusy) && !errors.Is(terminal, session.ErrReleaseAbandoned)
	}
	if f, ok := err.(*failure); ok && f.reason != "canceled" {
		return false
	}
	switch err {
	case nil, context.Canceled, cloud.ErrTransport, session.ErrTransport:
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if !onlyCancellationCauses(cause) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyCancellationCauses(wrapped.Unwrap())
	}
	return errors.Is(err, context.Canceled)
}

// LogFinal reports a command outcome once, including its final cleanup status.
// A previously reported primary terminal cause cannot suppress a new cleanup
// failure; in that case the command reports only the release cause.
func LogFinal(l *slog.Logger, message, phase string, err, cleanup error) {
	cleanup = CleanupError(cleanup)
	if l == nil || err == nil {
		return
	}
	cleanupOnly := cleanup != nil && !HasReceipt(err) && errors.Is(err, session.ErrStop)
	emitted := reportReceived(err, phase, func(phase, reason string) {
		if cleanupOnly {
			phase, reason = "release", CleanupClass(cleanup)
		}
		logFinalRecord(l, message, phase, reason, err, cleanup)
	})
	if cleanup != nil && !emitted {
		logFinalRecord(l, message, "release", CleanupClass(cleanup), cleanup, cleanup)
	}
}

// LogOutcome emits one final command outcome, even if its primary receipt was
// already observed by an owner. Unlike LogFinal's per-receipt diagnostics, this
// record summarizes the selected primary and final cleanup together.
func LogOutcome(l *slog.Logger, message, phase string, err, cleanup error) {
	if l == nil || err == nil {
		return
	}
	phase, reason := Fields(err, phase)
	cleanup = CleanupError(cleanup)
	if cleanup != nil && !HasReceipt(err) && errors.Is(err, session.ErrStop) {
		phase, reason = "release", CleanupClass(cleanup)
	}
	logFinalRecord(l, message, phase, reason, err, cleanup)
}
