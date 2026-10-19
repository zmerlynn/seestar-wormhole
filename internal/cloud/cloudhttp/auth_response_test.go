package cloudhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

// The TLS matrix covers real exchanges; injected bodies cover missing bodies
// and arbitrary reader errors whose secret material must not escape.
func TestAuthenticationStatusWithInjectedBodyErrors(t *testing.T) {
	for _, op := range []string{"detail", "online-link", "stop"} {
		for _, status := range []int{401, 403} {
			for _, mode := range []string{"valid", "truncated", "oversized", "canceled", "nil-body", "reader-error"} {
				t.Run(fmt.Sprintf("%s/%d/%s", op, status, mode), func(t *testing.T) {

					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					stops := 0
					b := online
					if op == "detail" {
						b = detail
					}
					if op == "stop" {
						b = `{"code":200,"data":null,"msg":"review-password-sentinel"}`
					}
					c, err := newAuthenticatedFixture(t, Config{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
						if strings.HasSuffix(r.URL.Path, "stop") && op != "stop" {
							stops++
							if r.Context().Err() != nil {
								t.Error("cleanup inherited cancellation")
							}
							return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":null,"msg":"ok"}`))}, nil
						}
						var body io.ReadCloser = io.NopCloser(strings.NewReader(b))
						switch mode {
						case "truncated":
							body = &terminalErrorBody{Reader: strings.NewReader(b), err: io.ErrUnexpectedEOF}
						case "oversized":
							body = io.NopCloser(strings.NewReader(b + strings.Repeat(" ", cloud.MaxBody)))
						case "canceled":
							body = &cancelReader{ReadCloser: body, cancel: cancel}
						case "nil-body":
							body = nil
						case "reader-error":
							body = &terminalErrorBody{Reader: strings.NewReader(b), err: errors.New("review-password-sentinel")}
						}
						return &http.Response{StatusCode: status, Body: body}, nil
					})})
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					switch op {
					case "detail":
						_, err = c.Detail(ctx, Device{Serial: "serial", Model: "S50 Pro"})
					case "online-link":
						var lease *Lease
						lease, err = c.Acquire(ctx, Device{Serial: "serial", Model: "S50 Pro"})
						if lease != nil {
							lease.Close()
							t.Error("auth failure handed off lease")
						}
						wantStops := 1
						if mode == "nil-body" || mode == "oversized" {
							wantStops = 0
						}
						if stops != wantStops {
							t.Errorf("cleanup count=%d, want %d", stops, wantStops)
						}
					case "stop":
						a, _, e := cloud.ParseOnline([]byte(online))
						if e != nil {
							t.Fatal(e)
						}
						err = c.stopWithContext(ctx, a.ID())
					}
					if !errors.Is(err, cloud.ErrUnauthorized) {
						t.Errorf("HTTP %d %s: got %v, want ErrUnauthorized", status, mode, err)
					}
					var bodyErr error
					switch mode {
					case "truncated", "nil-body", "reader-error":
						bodyErr = cloud.ErrTransport
					case "oversized":
						bodyErr = cloud.ErrInvalid
					case "canceled":
						bodyErr = context.Canceled
					}
					if bodyErr != nil && !errors.Is(err, bodyErr) {
						t.Errorf("got %v, want body class %v", err, bodyErr)
					}
					if err != nil && strings.Contains(err.Error(), "sentinel") {
						t.Errorf("error leaked sentinel: %v", err)
					}
				})
			}
		}
	}
}

type terminalErrorBody struct {
	io.Reader
	err error
}

func (b *terminalErrorBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		return n, b.err
	}
	return n, err
}
func (*terminalErrorBody) Close() error { return nil }

// The companion probe checks the HTTP capture boundary.
func TestExactHTTPBodyBudget(t *testing.T) {
	for _, size := range []int{cloud.MaxBody - 1, cloud.MaxBody, cloud.MaxBody + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {

			stops := 0
			b := online + strings.Repeat(" ", size-len(online))
			c, err := newAuthenticatedFixture(t, Config{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "stop") {
					stops++
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":null,"msg":"ok"}`))}, nil
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(b))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			lease, err := c.Acquire(t.Context(), Device{Serial: "serial", Model: "S50 Pro"})
			if lease != nil {
				defer lease.Close()
			}
			if size > cloud.MaxBody {
				if !errors.Is(err, cloud.ErrInvalid) || lease != nil || stops != 0 {
					t.Errorf("%d-byte body: err=%v lease=%t stops=%d, want ErrInvalid/no lease/0 stops", size, err, lease != nil, stops)
				}
			} else if err != nil || lease == nil {
				t.Errorf("%d-byte body: err=%v lease=%t, want valid lease", size, err, lease != nil)
			}
		})
	}
}
