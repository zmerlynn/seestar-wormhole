package cloudhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestAcquireSeparatesPrimaryAndCleanupErrors(t *testing.T) {

	for _, tt := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"invalid", `{"code":7788,"data":{"conId":"fixture"}}`, 200, cloud.ErrInvalid},
		{"busy", `{"code":300404,"data":{"conId":"fixture"}}`, 200, cloud.ErrBusy},
		{"unauthorized", `{"code":200,"data":{"conId":"fixture"}}`, 401, cloud.ErrUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				stops := 0
				c, err := newAuthenticatedFixture(t, Config{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/v1/device-p2p/stop" {
						stops++
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
				})})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				lease, err := c.Acquire(context.Background(), Device{"fixture", "fixture"})
				var failed *AcquireError
				if lease != nil || !errors.As(err, &failed) {
					t.Fatalf("lease/error=%v/%v, want nil/AcquireError", lease, err)
				}
				if !errors.Is(failed.Primary, tt.want) || errors.Is(failed.Primary, context.DeadlineExceeded) || errors.Is(failed.Primary, cloud.ErrTransport) {
					t.Errorf("primary=%v, want %v without cleanup causes", failed.Primary, tt.want)
				}
				if !errors.Is(failed.Cleanup, context.DeadlineExceeded) || errors.Is(failed.Cleanup, tt.want) {
					t.Errorf("cleanup=%v, want deadline without primary cause", failed.Cleanup)
				}
				if !errors.Is(err, tt.want) || !errors.Is(err, ErrCleanup) || !errors.Is(err, context.DeadlineExceeded) || stops != 1 {
					t.Errorf("aggregate=%v stops=%d, want original failure, cleanup, deadline and one stop", err, stops)
				}
			})
		})
	}
}
