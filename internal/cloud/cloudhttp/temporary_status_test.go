package cloudhttp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

type temporaryPageBody struct {
	closed bool
	mode   string
}

func (*temporaryPageBody) Read([]byte) (int, error) { panic("error page must not be read") }
func (b *temporaryPageBody) Close() error {
	b.closed = true
	switch b.mode {
	case "error":
		return io.ErrUnexpectedEOF
	case "panic":
		panic("close failed")
	}
	return nil
}

func TestTemporaryHTTPBodyLifetime(t *testing.T) {
	for _, path := range []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device", "/v1/my-device/detail"} {
		for _, status := range []int{429, 502, 503, 504} {
			for _, mode := range []string{"complete", "error", "panic"} {
				t.Run(fmt.Sprintf("%s/%d/%s", path, status, mode), func(t *testing.T) {
					page := &temporaryPageBody{mode: mode}
					var logs bytes.Buffer
					base, _ := accountOrigin("")
					c := &Client{base: base, logger: redactionTestLogger(&logs, slog.LevelDebug), transport: transportFunc(func(*http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: status, Body: page}, nil
					})}
					account := strings.HasPrefix(path, "/v1/user/")
					info, body, err := c.exchangeRaw(t.Context(), path, nil, nil, nil, account, true)
					if !page.closed || len(body) != 0 || info.status != status {
						t.Fatal("temporary response did not retain status and close its unread body")
					}
					want := cloud.Retryable
					if mode == "panic" {
						want = cloud.Fatal
					}
					// Parser errors from the discarded page cannot replace the status;
					// independent edge failures must still retain their severity.
					for _, got := range []error{err, responseError(info, body, cloud.ErrInvalid, err), accountResponseError(info, body, err, cloud.ErrRejected)} {
						if cloud.Disposition(got) != want || !errors.Is(got, cloud.ErrTransport) {
							t.Fatalf("temporary response=%v, disposition=%v, want %v", got, cloud.Disposition(got), want)
						}
					}
					c.rejected(t.Context(), "fixture", info, body, &cloud.ValidationError{Field: "body", Rule: "json"})
					if !strings.Contains(logs.String(), `"validation_field":"http_status"`) {
						t.Fatalf("temporary response diagnostics attributed failure to JSON: %s", logs.String())
					}
				})
			}
		}
	}
}

func TestUnrecognizedRequestKeepsStatusRejection(t *testing.T) {
	base, _ := accountOrigin("")
	c := &Client{base: base, transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("<html>service error</html>"))}, nil
	})}
	info, body, err := c.exchangeRaw(t.Context(), "/v1/unknown", nil, nil, nil, false, false)
	if cloud.Disposition(responseError(info, body, cloud.ErrInvalid, err)) != cloud.Denied {
		t.Fatal("unrecognized request became retryable")
	}
}
