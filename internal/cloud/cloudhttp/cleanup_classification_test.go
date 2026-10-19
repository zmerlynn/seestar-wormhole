package cloudhttp

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"io"
	"net/http"
	"strings"
	"testing"
)

type cleanupTransport struct{}

func (cleanupTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{"code":0,"data":{"conId":"owned"}}`
	status := 200
	if r.URL.Path == "/v1/device-p2p/stop" {
		body = `{"code":1}`
		status = 503
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
}
func TestAcquireRetainsCleanupClassification(t *testing.T) {

	c, err := newAuthenticatedFixture(t, Config{Transport: cleanupTransport{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Acquire(context.Background(), Device{"test", "test"})
	if !errors.Is(err, ErrCleanup) {
		t.Fatalf("cleanup failure not classified: %v", err)
	}
}

// Expiry applied before cleanup leaves stop unattempted, while preserving the
// unconfirmed-release marker and terminal reauthentication.
func TestAccountExpiryCleanupWithoutTokenRetainsClassification(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	stops := 0
	var c *Client
	var err error
	c, err = NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/user/login":
			return accountResponse(200, `{"code":0,"data":{"accessToken":"fixture-token"}}`), nil
		case "/v1/user/current-profile":
			return accountResponse(200, `{"code":0,"data":{"id":"fixture"}}`), nil
		case "/v1/device-p2p/online-link":
			c.remoteStatus(401, "fixture-token")
			if err := c.Login(t.Context()); !errors.Is(err, cloud.ErrReauthentication) {
				t.Error("expiry not applied before cleanup")
			}
			return accountResponse(401, `{"code":1,"data":{"conId":"fixture-allocation"}}`), nil
		case "/v1/device-p2p/stop":
			stops++
			if r.Header.Get("Authorization") != "fixture-token" {
				t.Error("stop lost allocation token snapshot")
			}
			return accountResponse(503, `{"code":1}`), nil
		default:
			t.Error("unexpected route")
			return accountResponse(500, `{}`), nil
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Acquire(t.Context(), Device{"fixture", "fixture"})
	if !errors.Is(err, ErrCleanup) || !errors.Is(err, cloud.ErrReauthentication) || stops != 0 {
		t.Fatalf("cleanup classification=%v stops=%d", err, stops)
	}
}
