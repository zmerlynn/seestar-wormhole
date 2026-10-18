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

func TestCloudTerminalRetainsBlockingCauses(t *testing.T) {
	for _, tc := range []struct {
		reason string
		cause  error
	}{
		{"rejected", cloud.ErrRejected},
		{"cloud_license_missing", cloud.ErrLicenseMissing},
		{"reauthentication_required", cloud.ErrReauthentication},
		{"invalid_configuration", cloud.ErrConfig},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			// A blocking cause must remain identifiable alongside an owed release.
			// Diagnostic metadata cannot weaken either policy obligation.
			outcome := cloud.JoinOutcomes(tc.cause, cloud.ErrOwedRelease)
			err := cloudTerminal(outcome)
			if phase, reason := diagnostic.Fields(err, ""); phase != "cloud" || reason != tc.reason {
				t.Errorf("terminal=%s/%s, want cloud/%s", phase, reason, tc.reason)
			}
			if !errors.Is(err, session.ErrUncertain) || !errors.Is(err, session.ErrReleaseAbandoned) || retryableCloud(outcome) {
				t.Errorf("blocking disposition or release marker lost: %v", err)
			}
		})
	}
}

type attackOfflineHTTP struct{}

func (attackOfflineHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":300406,"data":null,"msg":"PRIVATE_UID_PASSWORD_COORDINATE"}`))}, nil
}

func TestRealCloudConstructorRetainsOfflineCause(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		client, err := newFixtureClient(t, cloudhttp.Config{Transport: attackOfflineHTTP{}, Logger: diagnostic.Lifecycle(logger)})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s, err := start(ctx, Config{Logger: logger}, func(s *Session) sessionedge.Factory {
			return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "PRIVATE_DEVICE", Model: "S50 Pro"}}
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.DialPort(ctx, 445)
		if p, r := diagnostic.Fields(err, ""); p != "cloud" || r != "device_offline" {
			t.Errorf("real cloud cause=%s/%s (%v), want cloud/device_offline", p, r, err)
		}
		s.Close()
		if !strings.Contains(logs.String(), `"reason":"device_offline"`) {
			t.Errorf("offline cause absent from only default-visible report: %s", &logs)
		}
		if strings.Contains(logs.String(), "PRIVATE_") {
			t.Errorf("secret escaped: %s", &logs)
		}
	})
}
