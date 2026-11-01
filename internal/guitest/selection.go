//go:build guitest

package guitest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
)

// SelectTelescope exercises the real account selection adapter with an in-memory
// fake cloud. There are no sockets, environment credentials, or persisted tokens.
func SelectTelescope(ctx context.Context, d cloudhttp.Device) (cloudhttp.Device, error) {
	credentials, err := cloud.EmailCredentials("gui@example.invalid", "synthetic-password")
	if err != nil {
		return cloudhttp.Device{}, err
	}
	client, err := cloudhttp.NewWithCredentials(cloudhttp.Config{
		BaseURL: "https://cloud.example.invalid", AccountBaseURL: "https://account.example.invalid",
		Transport: selectionTransport{},
	}, credentials)
	if err != nil {
		return cloudhttp.Device{}, err
	}
	defer client.Close()
	return client.SelectDevice(ctx, d)
}

type selectionTransport struct{}

func (selectionTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body string
	switch r.URL.Path {
	case "/v1/user/login":
		body = `{"data":{"accessToken":"synthetic-token"}}`
	case "/v1/user/current-profile":
		body = `{"data":{"id":"synthetic-profile"}}`
	case "/v1/my-device":
		body = `{"data":{"items":[{"deviceSn":"synthetic-choice-1234","deviceModel":"Seestar S50 Pro","deviceStatus":1,"ssid":"synthetic"},{"deviceSn":"synthetic-choice-5678","deviceModel":"Future telescope\tmodel","deviceStatus":1,"ssid":"synthetic"}],"total":2}}`
	default:
		return nil, fmt.Errorf("unexpected fixture request")
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}
