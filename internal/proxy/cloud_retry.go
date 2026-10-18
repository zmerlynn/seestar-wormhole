package proxy

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func retryableCloud(err error) bool {
	return cloudOutcome(err) == session.ErrTransport
}

// cloudOutcome is the single translation from cloud disposition to session
// policy. All cloud observations have already used cloud.JoinOutcomes. The join
// here only attaches the session release marker; it never classifies causes.
func cloudOutcome(err error) error {
	if err == nil {
		return nil
	}
	joined := cloud.JoinOutcomes(err)
	if joined == nil {
		return nil
	}
	outcome := joined.(*cloud.Outcome)
	var result error
	switch outcome.Severity() {
	case cloud.Retryable:
		result = session.ErrTransport
	case cloud.OwedRelease:
		result = session.ErrTransport
	case cloud.Busy:
		result = session.ErrBusy
	default:
		result = session.ErrUncertain
	}
	if outcome.ReleaseBlocked() {
		result = session.ErrStop
	}
	if errors.Is(outcome, cloud.ErrOwedRelease) && result != session.ErrReleaseAbandoned {
		result = errors.Join(result, session.ErrReleaseAbandoned)
	}
	return result
}

// cloudTerminal retains only bounded attribution before cloudOutcome projects
// the joined disposition. Equality-based policy callers still use cloudOutcome.
func cloudTerminal(err error) error {
	result := cloudOutcome(err)
	if result == nil {
		return nil
	}
	// Cloud disposition is translated once; attribution, joined receipts and
	// cancellation then follow the same rule as every other terminal boundary.
	return diagnostic.AtLayer(err, session.Cloud, result)
}

// closeStartupCloud joins account publication before projecting the startup
// cause. Closing the client publishes admitted results even after cancellation.
func closeStartupCloud(ctx context.Context, c *cloudhttp.Client, err error) error {
	c.Close()
	err = cloudhttp.ResolveAccount(context.WithoutCancel(ctx), err)
	var option *diagnostic.OptionError
	if errors.As(err, &option) {
		// The command owns fixed option/rule diagnostics for catalog selection.
		return errors.Join(ctx.Err(), err)
	}
	return errors.Join(ctx.Err(), cloudTerminal(err))
}

func selectDevice(ctx context.Context, c *cloudhttp.Client, d cloudhttp.Device, logger *slog.Logger, rng session.RNG) (cloudhttp.Device, error) {
	cfg := session.DefaultConfig()
	var cap time.Duration
	for {
		selected, err := c.SelectDevice(ctx, d)
		// Request deadlines can precede account-owner publication. Join that
		// classification under startup ownership before choosing retry policy.
		err = cloudhttp.ResolveAccount(ctx, err)
		if ctx.Err() != nil {
			return cloudhttp.Device{}, diagnostic.Wrap("cloud", "", cloud.JoinOutcomes(err, ctx.Err()))
		}
		if err == nil || !retryableCloud(err) {
			return selected, err
		}
		var delay time.Duration
		delay, cap = session.RetryBackoff(cfg, rng, cap)
		if logger != nil {
			logger.Warn("cloud_retry", "phase", "device_selection", "reason", "transport", "retry_ms", delay.Milliseconds())
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return cloudhttp.Device{}, ctx.Err()
		case <-timer.C:
		}
	}
}
