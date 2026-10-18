package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type pendingAccountHTTP struct{ entered, reply chan struct{} }

func (h pendingAccountHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	close(h.entered)
	<-h.reply
	return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(`{"msg":"PRIVATE_COORDINATES_TIME_TOKEN_UID_PAYLOAD"}`))}, nil
}

func TestStartupJoinsPendingAccountBeforeAttribution(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "PRIVATE_PASSWORD")
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		h := pendingAccountHTTP{make(chan struct{}), make(chan struct{})}
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{StateDir: t.TempDir(), Transport: h, Logger: diagnostic.Lifecycle(logger)})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		selected := make(chan error, 1)
		go func() { _, err := selectDevice(ctx, c, cloudhttp.Device{}, logger, zeroRetryRNG{}); selected <- err }()
		<-h.entered
		cancel()
		err = <-selected
		if !errors.Is(err, cloud.ErrAccountUncertain) {
			t.Fatalf("selection=%v want pending account", err)
		}
		done := make(chan error, 1)
		go func() { done <- closeStartupCloud(ctx, c, err) }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("startup returned before account publication: %v", err)
		default:
		}
		close(h.reply)
		result := <-done
		if p, r := diagnostic.Fields(result, ""); p != "cloud" || r != "rejected" {
			t.Fatalf("startup=%s/%s want cloud/rejected", p, r)
		}
		if !errors.Is(result, context.Canceled) || !errors.Is(result, session.ErrUncertain) {
			t.Fatalf("startup policy/cancellation lost: %v", result)
		}
		diagnostic.LogFinal(logger, "proxy failed", "session", result, nil)
		diagnostic.LogFinal(logger, "proxy failed", "session", result, nil)
		if strings.Count(logs.String(), `"level":"ERROR"`) != 1 || strings.Contains(logs.String(), "PRIVATE_") {
			t.Fatalf("startup report=%s want one safe ERROR", &logs)
		}
	})
}
