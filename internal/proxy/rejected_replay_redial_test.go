package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

type rejectedReplayBody struct{ ctx context.Context }

func (b rejectedReplayBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (rejectedReplayBody) Close() error               { return nil }

func TestRejectedReplayDoesNotRedialCredentials(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "review@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "review-password")

	synctest.Test(t, func(t *testing.T) {
		var logins, profiles atomic.Int32
		repeated := make(chan struct{})
		c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			status := 200
			body := `{"code":300404,"data":null}`
			switch r.URL.Path {
			case "/v1/user/login":
				n := logins.Add(1)
				if n == 2 {
					return &http.Response{StatusCode: 403, Body: rejectedReplayBody{r.Context()}}, nil
				}
				if n == 3 {
					close(repeated)
				}
				body = `{"code":0,"data":{"accessToken":"review-token"}}`
			case "/v1/user/current-profile":
				if profiles.Add(1) == 1 {
					status = 401
					body = `{"code":401,"data":null}`
				} else {
					body = `{"code":0,"data":{"id":"review-user"}}`
				}
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		s, err := start(context.Background(), Config{}, func(s *Session) sessionedge.Factory {
			return &factory{s: s, cloud: c, device: cloudhttp.Device{Serial: "review", Model: "S50 Pro"}}
		})
		if err != nil {
			c.Close()
			t.Fatal(err)
		}
		defer s.Close()
		select {
		case <-repeated:
			t.Fatalf("daemon automatically retried received replay HTTP 403: logins=%d; want 2 without operator credential correction", logins.Load())
		case <-time.After(30 * time.Second):
			if logins.Load() != 2 {
				t.Fatalf("fixture logins=%d, want 2", logins.Load())
			}
		}
	})
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
