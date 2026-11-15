package cloudhttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

type causeTransport struct {
	err  error
	body bool
}
type causeReader struct{ err error }

func (r causeReader) Read([]byte) (int, error) { return 0, r.err }
func (f causeTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if f.body {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(causeReader{f.err})}, nil
	}
	return nil, f.err
}

// Typed transport failures are checked through response reads and URL wrappers.
// Their text shows, host included; the URL's userinfo and query do not.
func TestHTTPTransportCauses(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	for _, tc := range []struct {
		name, want string
		err        error
	}{
		{"refused", "connection_refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}},
		{"dns", "dns_not_found", &net.DNSError{Name: "cloud.example", Err: "no such host", IsNotFound: true}},
		{"timeout", "timeout", &net.DNSError{Name: "cloud.example", Err: "i/o timeout", IsTimeout: true}},
		{"unknown", "transport_failed", errors.New("transport broke")},
	} {
		for _, body := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/dial", true: "/read"}[body], func(t *testing.T) {
				var logs bytes.Buffer
				logger := diagnostic.Logger("json", &logs, slog.LevelDebug)
				wrapped := &url.Error{Op: "Get", URL: "https://user:password-sentinel@cloud.example/v1/my-device?token=token-sentinel", Err: tc.err}
				c, err := newAuthenticatedFixture(t, Config{Transport: causeTransport{wrapped, body}, Logger: logger})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				ctx := context.Background()
				_, err = c.SelectDevice(ctx, Device{})
				if !errors.Is(err, cloud.ErrTransport) || ctx.Err() != nil {
					t.Fatalf("got %v; want transport failure with live context", err)
				}
				diagnostic.Log(logger, "proxy failed", "session", err)
				for _, phase := range []string{"device_list", "device_selection", "session"} {
					want := `"phase":"` + phase + `","reason":"` + tc.want + `"`
					if !strings.Contains(logs.String(), want) {
						t.Fatalf("logs = %s; want %s", &logs, want)
					}
				}
				if !strings.Contains(err.Error(), tc.err.Error()) || !strings.Contains(logs.String(), `"https://cloud.example/v1/my-device`) {
					t.Fatalf("transport text missing: %v %s", err, &logs)
				}
				for _, secret := range []string{"password-sentinel", "token-sentinel"} {
					if strings.Contains(logs.String()+err.Error(), secret) {
						t.Fatalf("leaked %s", secret)
					}
				}
			})
		}
	}
}
