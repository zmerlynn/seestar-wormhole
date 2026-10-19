package proxy

import (
	"bytes"
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
)

type independentSelectionBody struct {
	io.Reader
	cancel context.CancelFunc
}

func (b independentSelectionBody) Close() error { b.cancel(); return nil }

type independentSelectionHTTP struct{ cancel context.CancelFunc }

func (h independentSelectionHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: independentSelectionBody{strings.NewReader(`{"code":300406,"data":null,"msg":"PRIVATE_COORDINATES_TIME_TOKEN_UID_PAYLOAD"}`), h.cancel}}, nil
}
func TestIndependentSelectionReceivedCauseCancels(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		c, err := newFixtureClient(t, cloudhttp.Config{Transport: independentSelectionHTTP{cancel}, Logger: diagnostic.Lifecycle(logger)})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_, err = selectDevice(ctx, c, cloudhttp.Device{}, logger, zeroRetryRNG{})
		if !errors.Is(err, cloud.ErrOffline) {
			t.Errorf("received device-selection offline discarded: result=%v", err)
		}
		diagnostic.LogFinal(logger, "proxy failed", "session", err, nil)
		if !strings.Contains(logs.String(), `"reason":"device_offline"`) {
			t.Errorf("received cause absent from command report: %s", &logs)
		}
		if strings.Contains(logs.String(), "PRIVATE_") {
			t.Errorf("private data escaped: %s", &logs)
		}
	})
}
func TestIndependentCloudCausePrivacy(t *testing.T) {
	for _, cause := range []error{cloud.ErrUnauthorized, cloud.ErrReauthentication, cloud.ErrBusy, cloud.ErrOffline, cloud.ErrRejected, cloud.ErrInvalid, cloud.ErrConfig, cloud.ErrInternal} {
		got := cloudTerminal(cloud.JoinOutcomes(cause, context.Canceled, errors.New("PRIVATE_COORDINATES_TIME_TOKEN_UID_PAYLOAD")))
		if p, r := diagnostic.Fields(got, ""); p != "cloud" || r == "canceled" {
			t.Errorf("known cause erased=%s/%s", p, r)
		}
		if !errors.Is(got, session.ErrUncertain) {
			t.Errorf("fatal unknown outcome weakened: %v", got)
		}
		var logs bytes.Buffer
		diagnostic.Log(slog.New(slog.NewJSONHandler(&logs, nil)), "layer_failed", "", got)
		if strings.Contains(logs.String(), "PRIVATE_") {
			t.Error("private data escaped")
		}
	}
}
