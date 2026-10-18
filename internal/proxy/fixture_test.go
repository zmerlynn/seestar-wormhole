package proxy

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
)

type fixtureAccountTransport struct{ cloudhttp.Transport }

func (f fixtureAccountTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body string
	switch r.URL.Path {
	case "/v1/user/login":
		body = `{"code":0,"data":{"accessToken":"fixture-token"}}`
	case "/v1/user/current-profile":
		body = `{"code":0,"data":{"id":"fixture-account"}}`
	default:
		return f.Transport.RoundTrip(r)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

// Allocation fixtures receive an account login independently of their failure routes.
func newFixtureClient(t *testing.T, cfg cloudhttp.Config) (*cloudhttp.Client, error) {
	t.Helper()
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	cfg.Transport = fixtureAccountTransport{cfg.Transport}
	return cloudhttp.NewFromEnvironment(cfg)
}

// expiredAllocationTransport establishes a real remote 401 and joins the account
// owner before returning an allocation's 401. This fixes the ordering explicitly:
// cleanup must see no token, without relying on which goroutine is scheduled.
type expiredAllocationTransport struct {
	cloudhttp.Transport
	client *cloudhttp.Client
	t      *testing.T
}

func (f *expiredAllocationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/v1/my-device/detail" && r.URL.Query().Get("deviceSn") == "expire-fixture" {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"code":401,"data":null}`))}, nil
	}
	reply, err := f.Transport.RoundTrip(r)
	if reply != nil && reply.StatusCode == 401 && r.URL.Path == "/v1/device-p2p/online-link" {
		_, expired := f.client.Detail(r.Context(), cloudhttp.Device{Serial: "expire-fixture", Model: "S50 Pro"})
		if !errors.Is(expired, cloud.ErrReauthentication) {
			f.t.Error("fixture remote expiry lost")
		}
		if err := f.client.Login(r.Context()); !errors.Is(err, cloud.ErrReauthentication) {
			f.t.Error("fixture account expiry not joined")
		}
	}
	return reply, err
}

func newExpiredAllocationFixtureClient(t *testing.T, cfg cloudhttp.Config) (*cloudhttp.Client, error) {
	t.Helper()
	edge := &expiredAllocationTransport{Transport: cfg.Transport, t: t}
	cfg.Transport = edge
	client, err := newFixtureClient(t, cfg)
	edge.client = client
	return client, err
}
