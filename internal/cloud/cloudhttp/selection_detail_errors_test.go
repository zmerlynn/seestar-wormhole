package cloudhttp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func TestSelectionDetailErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		transport  error
		want       error
	}{
		{name: "transport", transport: errors.New("synthetic transport"), want: cloud.ErrTransport},
		{name: "timeout", transport: context.DeadlineExceeded, want: context.DeadlineExceeded},
		{name: "auth", body: detail, status: 401, want: cloud.ErrUnauthorized},
		{name: "forbidden", body: detail, status: 403, want: cloud.ErrUnauthorized},
		{name: "server", body: detail, status: 500, want: cloud.ErrRejected},
		{name: "null", body: `{"code":200,"data":null}`, status: 200, want: cloud.ErrInvalid},
		{name: "undocumented code", body: `{"code":404,"data":null}`, status: 200, want: cloud.ErrInvalid},
		{name: "busy", body: strings.Replace(detail, `"deviceStatus":1`, `"deviceStatus":4`, 1), status: 200, want: cloud.ErrBusy},
		{name: "offline", body: strings.Replace(detail, `"deviceStatus":1`, `"deviceStatus":2`, 1), status: 200, want: cloud.ErrOffline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			f := &selectionCloud{bodies: []string{pageA1}, detailBody: tc.body}
			c := selectionClient(t, f, &logs, slog.LevelInfo)
			c.transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/v1/my-device/detail" {
					if tc.transport != nil {
						return nil, tc.transport
					}
					return accountResponse(tc.status, tc.body), nil
				}
				return f.RoundTrip(r)
			})
			// Compare the fallback with the same Detail operation, including classification.
			_, direct := c.Detail(t.Context(), Device{"serial", "S50 Pro"})
			c.account.close()
			c = selectionClient(t, f, &logs, slog.LevelInfo)
			c.transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/v1/my-device/detail" {
					if tc.transport != nil {
						return nil, tc.transport
					}
					return accountResponse(tc.status, tc.body), nil
				}
				return f.RoundTrip(r)
			})
			_, got := c.SelectDevice(t.Context(), Device{"serial", "S50 Pro"})
			if !errors.Is(got, tc.want) || errors.Is(got, cloud.ErrConfig) || strings.Contains(got.Error(), "not found on account") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			gp, gr := diagnostic.Fields(got, "")
			dp, dr := diagnostic.Fields(direct, "")
			if gp != dp || gr != dr {
				t.Fatalf("classification %s/%s, want %s/%s", gp, gr, dp, dr)
			}
		})
	}
}
