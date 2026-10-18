package cloudhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

type rejectionDeadlineBody struct{ ctx context.Context }

func (b rejectionDeadlineBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (rejectionDeadlineBody) Close() error               { return nil }

func TestReceivedReplayRejectionRemainsTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Setenv("SEESTAR_EMAIL", "review@example.invalid")
		t.Setenv("SEESTAR_PASSWORD", "review-password")

		logins := 0
		c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "login") {
				logins++
				if logins == 2 {
					return &http.Response{StatusCode: 403, Body: rejectionDeadlineBody{r.Context()}}, nil
				}
				return accountResponse(200, `{"code":0,"data":{"accessToken":"review-token"}}`), nil
			}
			return accountResponse(401, `{"code":401,"data":null}`), nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		err = c.Login(t.Context())
		if !errors.Is(err, cloud.ErrRejected) {
			t.Fatalf("first reply lost rejection: %v", err)
		}
		err = c.Login(t.Context())
		if logins != 2 {
			t.Fatalf("received replay HTTP 403 was retried: logins=%d error=%v; want 2", logins, err)
		}
	})
}

func TestReceivedRejectionBodyOutcomes(t *testing.T) {
	for _, rounds := range []int{0, 1, 2} {
		for _, status := range []int{401, 403} {
			for _, mode := range []string{"timeout", "canceled", "truncated", "nil-body", "oversized"} {
				t.Run(fmt.Sprintf("replays=%d/status=%d/%s", rounds, status, mode), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
						t.Setenv("SEESTAR_PASSWORD", "fixture-password")

						logins := 0
						entered := make(chan struct{})
						ctx, cancel := context.WithCancel(t.Context())
						defer cancel()
						c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
							if strings.HasSuffix(r.URL.Path, "login") {
								logins++
								if logins <= rounds {
									return accountResponse(200, `{"code":0,"data":{"accessToken":"challenged"}}`), nil
								}
								var body io.ReadCloser = &terminalErrorBody{Reader: strings.NewReader(`{"code":7788,"data":`), err: io.ErrUnexpectedEOF}
								switch mode {
								case "timeout", "canceled":
									body = rejectionBarrierBody{ctx: r.Context(), entered: entered}
								case "nil-body":
									body = nil
								case "oversized":
									body = io.NopCloser(strings.NewReader(strings.Repeat(" ", cloud.MaxBody+1)))
								}
								return &http.Response{StatusCode: status, Body: body}, nil
							}
							return accountResponse(401, `{"code":401,"data":null}`), nil
						})})
						if err != nil {
							t.Fatal(err)
						}
						defer c.Close()
						done := make(chan error, 1)
						go func() { done <- c.Login(ctx) }()
						if mode == "canceled" {
							<-entered
							cancel()
						}
						<-done
						synctest.Wait() // Join the owner even if caller cancellation won its reply.
						err = c.Login(t.Context())
						var ae *cloud.AccountError
						if rounds == 0 {
							if !errors.Is(err, cloud.ErrRejected) || !errors.As(err, &ae) || ae.Status != status {
								t.Fatalf("initial rejection lost: %v", err)
							}
						} else if !errors.Is(err, cloud.ErrReauthentication) || errors.As(err, &ae) || errors.Is(err, cloud.ErrRejected) {
							t.Fatalf("later operation inferred replay cause: %v", err)
						}
						if logins != rounds+1 {
							t.Fatalf("rejection retried: logins=%d, want %d", logins, rounds+1)
						}
					})
				})
			}
		}
	}
}

type rejectionBarrierBody struct {
	ctx     context.Context
	entered chan struct{}
}

func (b rejectionBarrierBody) Read([]byte) (int, error) {
	close(b.entered)
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (rejectionBarrierBody) Close() error { return nil }

// The raw edge must already classify rejection before its caller parses a body.
func TestLoginHeadersClassifyRejection(t *testing.T) {
	for _, status := range []int{401, 403} {
		for _, mode := range []string{"truncated", "canceled", "nil-body"} {
			t.Run(fmt.Sprintf("%d/%s", status, mode), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				base, _ := accountOrigin("")
				c := &Client{base: base, transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					var body io.ReadCloser = &terminalErrorBody{Reader: strings.NewReader(`{"code":7788,`), err: io.ErrUnexpectedEOF}
					if mode == "canceled" {
						body = &cancelReader{ReadCloser: body, cancel: cancel}
					}
					if mode == "nil-body" {
						body = nil
					}
					return &http.Response{StatusCode: status, Body: body}, nil
				})}
				got, _, err := c.exchangeRaw(ctx, "/v1/user/login", nil, []byte(`{}`), nil, true, true)
				var ae *cloud.AccountError
				if got.status != status || !errors.Is(err, cloud.ErrRejected) || !errors.As(err, &ae) || ae.Status != status {
					t.Fatalf("received rejection lost before parsing: status=%d error=%v", got.status, err)
				}
			})
		}
	}
}
