package cloudhttp

import (
	"context"
	"errors"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

// Each admitted job has one immutable publication. Closing done transfers the
// owner's result to any number of waiters, without a reply queue or goroutine.
type accountCompletion struct {
	done   chan struct{}
	result accountReply
}

func newAccountCompletion() *accountCompletion {
	return &accountCompletion{done: make(chan struct{})}
}
func (c *accountCompletion) publish(r accountReply) {
	c.result = r
	close(c.done)
}

type pendingAccount struct{ completion *accountCompletion }

func (*pendingAccount) Error() string        { return "cloud: account outcome pending" }
func (*pendingAccount) Is(target error) bool { return target == cloud.ErrAccountUncertain }

// ResolveAccount waits for any provisional account results in err. The account
// owner publishes the eventual classification before admitting more work. Only
// that provisional observation is replaced; all known causes and release
// metadata remain joined. Cancellation leaves the pending handle intact, so an
// unresolved result never becomes permission to retry. No new request is issued.
// Daemon workers use their lifetime context, not the expired request context.
func ResolveAccount(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var handle *pendingAccount
	if !errors.As(err, &handle) {
		return err
	}
	if pending, ok := err.(*pendingAccount); ok {
		select {
		case <-pending.completion.done:
			return pending.completion.result.err
		default:
		}
		select {
		case <-pending.completion.done:
			return pending.completion.result.err
		case <-ctx.Done():
			return cloud.JoinOutcomes(pending, ctx.Err())
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := append([]error(nil), joined.Unwrap()...)
		for i, cause := range causes {
			causes[i] = ResolveAccount(ctx, cause)
		}
		result := cloud.JoinOutcomes(causes...)
		if outcome, ok := err.(*cloud.Outcome); ok && outcome.ReleaseBlocked() {
			// Retain the failed-release disposition while joining the account
			// publication too; a known denial cannot hide a later fatal cause.
			result = cloud.ReleaseOutcome(result)
		}
		return result
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok && errors.Is(err, cloud.ErrAccountUncertain) {
		return ResolveAccount(ctx, wrapped.Unwrap())
	}
	return err
}
