package cloud

import (
	"context"
	"errors"
	"net"
	"syscall"
)

// Severity is the total local disposition order. Busy is a received denial with
// its bounded retry policy; Denied and Fatal block without that window.
// Successful observations are the identity. No join can decrease severity.
type Severity uint8

const (
	// Success means no failure was observed.
	Success Severity = iota
	// Retryable covers transport loss, cancellation and offline devices.
	Retryable
	// OwedRelease retains an unconfirmed bounded stop.
	OwedRelease
	// Malformed means validation failed or canceled account completion is unknown.
	Malformed
	// Busy is a received refusal governed by the bounded retry policy.
	Busy
	// Denied is a received authorization or status rejection.
	Denied
	// Fatal is an internal failure or invalid local configuration.
	Fatal
)

// ErrOwedRelease records a bounded stop whose acceptance is unconfirmed.
var ErrOwedRelease = errors.New("cloud: allocation cleanup failed")

// ErrInternal records an internal edge failure, including a recovered panic.
var ErrInternal = errors.New("cloud: internal failure")

// ErrAccountUncertain marks cancellation after admission without a known reply.
// The owner still joins the eventual response before accepting later work.
var ErrAccountUncertain = errors.New("cloud: account outcome uncertain")

// Outcome accumulates sanitized observed causes under one severity order:
// fatal > denied > busy > malformed > owed-release > retryable > success.
// Release metadata is orthogonal: a failed release must remain recorded even
// when an acquisition denial dominates it. Fields are private so callers cannot
// manufacture or lower a disposition. Use JoinOutcomes at every combination.
type Outcome struct {
	severity       Severity
	releaseBlocked bool
	causes         []error
}

// Error returns a fixed diagnostic without formatting causes.
func (*Outcome) Error() string { return "cloud: attempt failed" }

// Unwrap retains all observed causes and protects the accumulator slice.
func (o *Outcome) Unwrap() []error { return append([]error(nil), o.causes...) }

// Severity returns the strongest observed disposition.
func (o *Outcome) Severity() Severity { return o.severity }

// ReleaseBlocked reports a failed stop with a blocking disposition.
func (o *Outcome) ReleaseBlocked() bool { return o.releaseBlocked }

// JoinOutcomes is the sole outcome join: max severity, union release metadata,
// and retain every cause. It is associative, commutative and idempotent in
// disposition. It returns nil for success, avoiding a typed-nil error.
// Wrapped/joined legacy errors are inspected leaf by leaf; a transport cause
// cannot hide malformed input, a received denial, or an internal failure.
func JoinOutcomes(observed ...error) error {
	o := new(Outcome)
	var observe func(error)
	observe = func(err error) {
		if err == nil {
			return
		}
		if prior, ok := err.(*Outcome); ok {
			o.severity = max(o.severity, prior.severity)
			o.releaseBlocked = o.releaseBlocked || prior.releaseBlocked
			o.causes = append(o.causes, prior.causes...)
			return
		}
		// ParseLogin/ParseProfile with no response describe an absent exchange,
		// not observed invalid JSON or a received HTTP rejection.
		if account, ok := err.(*AccountError); ok && account.Status == 0 {
			return
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, cause := range joined.Unwrap() {
				observe(cause)
			}
			o.causes = append(o.causes, err)
			return
		}
		if wrapped, ok := err.(interface{ Unwrap() error }); ok {
			observe(wrapped.Unwrap())
			// Preserve typed diagnostics without treating their wrapper as an outcome.
			o.causes = append(o.causes, err)
			return
		}
		rank := Fatal // Unknown internal errors are conservative, never retryable.
		switch err {
		case ErrTransport, ErrOffline, context.Canceled, context.DeadlineExceeded, syscall.ECONNREFUSED:
			rank = Retryable
		case ErrOwedRelease:
			rank = OwedRelease
		case ErrInvalid, ErrLicenseMissing, ErrAccountUncertain:
			rank = Malformed
		case ErrBusy:
			rank = Busy
		case ErrUnauthorized, ErrReauthentication, ErrRejected:
			rank = Denied
		default:
			if nativeRefusal(err) {
				rank = Retryable
			}
			if errors.Is(err, ErrAccountUncertain) {
				rank = Malformed
			}
			var dns *net.DNSError
			if errors.As(err, &dns) && dns.IsNotFound {
				rank = Retryable
			}
		}
		o.severity = max(o.severity, rank)
		o.causes = append(o.causes, err)
	}
	for _, err := range observed {
		observe(err)
	}
	if len(o.causes) == 0 {
		return nil
	}
	return o
}

// ReleaseOutcome records a failed stop using the same join as acquisition.
// A received denial/malformed stop blocks replacement; pure I/O abandonment
// remains recoverable after an unconfirmed stop. Call only after bounded stop.
func ReleaseOutcome(err error) error {
	if err == nil {
		return nil
	}
	joined := JoinOutcomes(err)
	if joined == nil {
		return nil
	}
	result := joined.(*Outcome)
	result.releaseBlocked = result.severity >= Malformed
	return JoinOutcomes(result, ErrOwedRelease)
}

// Disposition returns the joined severity, including for legacy error trees.
func Disposition(err error) Severity {
	if result := JoinOutcomes(err); result != nil {
		return result.(*Outcome).severity
	}
	return Success
}
