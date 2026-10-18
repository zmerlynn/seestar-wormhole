package proxy

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestEnvironmentMissingSelectorsUsesCatalog(t *testing.T) {

	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")

	synctest.Test(t, func(t *testing.T) {
		// A canceled discovery cannot contact any endpoint and must join its newly
		// constructed account owner. Missing selectors are no longer a config error.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s, err := New(ctx, Config{Idle: time.Second})
		if s != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("got session=%v error=%v, want cancellation", s, err)
		}
	})
}
