package cloudhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

// Exercises every exchange and cancellation. HTTP 401 and the local HTTP 403
// authorization policy apply independently of body parsing; captured IDs still
// require cleanup.
func TestResponseStatusWithBodyErrors(t *testing.T) {
	for _, operation := range []string{"detail", "online-link", "stop"} {
		for _, status := range []int{200, 401, 403, 503} {
			for _, mode := range []string{"valid", "truncated", "oversized", "canceled"} {
				t.Run(fmt.Sprintf("%s/%d/%s", operation, status, mode), func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					var requests, stops atomic.Int32
					c := setup(t, func(w http.ResponseWriter, r *http.Request) {
						if strings.HasSuffix(r.URL.Path, "stop") {
							stops.Add(1)
							if operation != "stop" {
								if r.Context().Err() != nil {
									t.Error("cleanup inherited attempt cancellation")
								}
								b, _ := io.ReadAll(r.Body)
								if string(b) != `{"conId":"allocation-secret"}` {
									t.Errorf("cleanup body = %q", b)
								}
								io.WriteString(w, `{"code":200,"data":null,"msg":"ok"}`)
								return
							}
						}
						requests.Add(1)
						body := online
						switch operation {
						case "detail":
							body = detail
						case "stop":
							body = `{"code":200,"data":null,"msg":"token-secret"}`
						}
						switch mode {
						case "truncated":
							w.Header().Set("Content-Length", "1000")
						case "oversized":
							body += strings.Repeat("x", cloud.MaxBody)
						}
						w.WriteHeader(status)
						io.WriteString(w, body)
					})
					if mode == "canceled" {
						base := c.transport
						c.transport = transportFunc(func(r *http.Request) (*http.Response, error) {
							resp, err := base.RoundTrip(r)
							if err == nil && strings.HasSuffix(r.URL.Path, operation) {
								resp.Body = &cancelReader{ReadCloser: resp.Body, cancel: cancel}
							}
							return resp, err
						})
					}
					var err error
					switch operation {
					case "detail":
						_, err = c.Detail(ctx, Device{Serial: "serial", Model: "S50 Pro"})
					case "online-link":
						var lease *Lease
						lease, err = c.Acquire(ctx, Device{Serial: "serial", Model: "S50 Pro"})
						if lease != nil {
							defer lease.Close()
							if status != 200 || mode != "valid" {
								t.Error("failed acquisition handed off a lease")
							}
						}
						if status != 200 || mode != "valid" {
							want := int32(1)
							if mode == "oversized" {
								want = 0
							}
							if stops.Load() != want {
								t.Errorf("recovered-ID stops = %d, want %d", stops.Load(), want)
							}
						}
					case "stop":
						a, _, parseErr := cloud.ParseOnline([]byte(online))
						if parseErr != nil {
							t.Fatal(parseErr)
						}
						err = c.stopWithContext(ctx, a.ID())
					}
					var bodyErr error
					switch mode {
					case "truncated":
						bodyErr = cloud.ErrTransport
					case "oversized":
						bodyErr = cloud.ErrInvalid
					case "canceled":
						bodyErr = context.Canceled
					}
					temporary := operation == "detail" && status == 503
					if temporary {
						bodyErr = nil // Error-page bodies are closed without reading.
						if cloud.Disposition(err) != cloud.Retryable {
							t.Errorf("temporary detail status=%v, want retryable", err)
						}
					}
					if errors.Is(err, cloud.ErrUnauthorized) != (status == 401 || status == 403) {
						t.Errorf("HTTP %d: got %v; unauthorized = %t, want %t", status, err, errors.Is(err, cloud.ErrUnauthorized), status == 401 || status == 403)
					}
					if bodyErr != nil && !errors.Is(err, bodyErr) {
						t.Errorf("got %v, want body error %v", err, bodyErr)
					}
					if mode == "valid" {
						if status == 200 && err != nil {
							t.Errorf("valid response: %v", err)
						}
						if status == 503 && !temporary && !errors.Is(err, cloud.ErrRejected) {
							t.Errorf("got %v, want ErrRejected", err)
						}
					}
					if requests.Load() != 1 {
						t.Errorf("requests = %d, want 1", requests.Load())
					}
					if err != nil && strings.Contains(err.Error(), "secret") {
						t.Errorf("error leaked response material: %v", err)
					}
				})
			}
		}
	}
}
