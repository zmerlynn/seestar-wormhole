package proxy

import (
	"bytes"
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
)

type cancelResponseBody struct {
	io.Reader
	cancel  context.CancelFunc
	attempt context.Context
}

func (b *cancelResponseBody) Close() error {
	b.cancel()
	// Wait until the owner has canceled the request context. The complete body
	// is already read; this forces the received outcome and cancellation to join.
	<-b.attempt.Done()
	return nil
}

type cancelResponseTransport struct{ cancel context.CancelFunc }

func (h cancelResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: &cancelResponseBody{strings.NewReader(`{"code":300406,"data":null,"msg":"PRIVATE_COORDINATES_TIME_TOKEN_UID_PAYLOAD"}`), h.cancel, req.Context()}}, nil
}

func TestOfflineResponseRacesCancellation(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		client, err := newFixtureClient(t, cloudhttp.Config{Transport: cancelResponseTransport{cancel}, Logger: diagnostic.Lifecycle(logger)})
		if err != nil {
			t.Fatal(err)
		}
		s, err := start(ctx, Config{Logger: logger}, func(s *Session) sessionedge.Factory {
			return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "PRIVATE_DEVICE", Model: "S50 Pro"}}
		})
		if err != nil {
			t.Fatal(err)
		}
		<-s.Done()
		err = s.Close()
		if p, r := diagnostic.Fields(err, ""); p != "cloud" || r != "device_offline" {
			t.Errorf("owner terminal=%s/%s (%v), want cloud/device_offline", p, r, err)
		}
		if strings.Count(logs.String(), `"level":"ERROR"`) != 1 || !strings.Contains(logs.String(), `"reason":"device_offline"`) {
			t.Errorf("known offline response lost when body Close canceled parent: %s", &logs)
		}
		if strings.Contains(logs.String(), "PRIVATE_") {
			t.Errorf("privacy failure: %s", &logs)
		}
	})
}

func TestCloudCauseDominatesCancellation(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		reason string
		policy error
	}{
		{cloud.ErrOffline, "device_offline", session.ErrTransport},
		{cloud.ErrRejected, "rejected", session.ErrUncertain},
		{cloud.ErrBusy, "busy", session.ErrBusy},
		{cloud.ErrInvalid, "invalid_response", session.ErrUncertain},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			err := cloudTerminal(cloud.JoinOutcomes(tc.cause, context.Canceled))
			if p, r := diagnostic.Fields(err, ""); p != "cloud" || r != tc.reason {
				t.Errorf("joined cause=%s/%s, want cloud/%s", p, r, tc.reason)
			}
			if !errors.Is(err, tc.policy) {
				t.Errorf("policy changed: %v", err)
			}
		})
	}
}

func TestCloudCancellationAndReleaseMetadata(t *testing.T) {
	for _, err := range []error{context.Canceled, cloud.JoinOutcomes(context.Canceled, cloud.ErrTransport)} {
		if phase, reason := diagnostic.Fields(cloudTerminal(err), ""); phase != "cloud" || reason != "canceled" {
			t.Errorf("operator cancellation=%s/%s, want cloud/canceled", phase, reason)
		}
	}
	err := cloudTerminal(cloud.JoinOutcomes(cloud.ErrOffline, context.Canceled, cloud.ReleaseOutcome(cloud.ErrRejected)))
	if phase, reason := diagnostic.Fields(err, ""); phase != "cloud" || reason == "canceled" {
		t.Errorf("received outcome=%s/%s, want a known cloud cause", phase, reason)
	}
	if !errors.Is(err, session.ErrStop) || !errors.Is(err, session.ErrReleaseAbandoned) {
		t.Errorf("release metadata lost: %v", err)
	}
	if cloudOutcome(cloud.JoinOutcomes(cloud.ErrOffline, context.Canceled)) != session.ErrTransport {
		t.Fatal("equality-based policy changed")
	}
}
