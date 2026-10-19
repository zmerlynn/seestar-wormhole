package cloudhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

const online = `{"code":200,"data":{"conId":"allocation-secret","deviceUid":"uid-secret","sdkLicenseKey":"opaque-license","username":"account-secret","password":"password-secret","p2pFactory":"TUTK","p2pLicenseKey":"unused","p2pStatus":1},"msg":"ok"}`
const detail = `{"code":200,"data":{"deviceSn":"serial","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"},"msg":"ok"}`

func setup(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()

	s := httptest.NewTLSServer(handler)
	t.Cleanup(s.Close)
	c, err := newAuthenticatedFixture(t, Config{BaseURL: s.URL, Transport: s.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func TestLifecycle(t *testing.T) {
	var details, allocs, stops atomic.Int32
	c := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token-secret" || r.Header.Get("x-service-platform") != "seestar" {
			t.Error("auth headers")
		}
		switch r.URL.Path {
		case "/v1/my-device/detail":
			details.Add(1)
			if r.URL.Query().Get("deviceSn") != "serial" || r.URL.Query().Get("deviceModel") != "S50 Pro" {
				t.Error("detail query")
			}
			io.WriteString(w, detail)
		case "/v1/device-p2p/online-link":
			allocs.Add(1)
			if r.URL.Query().Get("srcId") != "source-secret" {
				t.Error("source query")
			}
			io.WriteString(w, online)
		case "/v1/device-p2p/stop":
			stops.Add(1)
			b, _ := io.ReadAll(r.Body)
			if r.Method != "POST" || string(b) != `{"conId":"allocation-secret"}` {
				t.Error("stop wire")
			}
			io.WriteString(w, `{"code":200,"data":null,"msg":"ok"}`)
		default:
			t.Error("unexpected route")
		}
	})
	d := Device{Serial: "serial", Model: "S50 Pro"}
	if _, err := c.Detail(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	lease, err := c.Acquire(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if string(lease.Allocation().License()) != "opaque-license" {
		t.Error("opaque license")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if details.Load() != 1 || allocs.Load() != 1 || stops.Load() != 1 {
		t.Fatalf("lifecycle counts %d/%d/%d", details.Load(), allocs.Load(), stops.Load())
	}
}
func TestFailureCleanup(t *testing.T) {
	for _, mode := range []string{"truncated", "malformed", "oversized", "busy-with-id", "http-error", "read-cancel"} {
		t.Run(mode, func(t *testing.T) {
			var stops atomic.Int32
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := setup(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/my-device/detail":
					io.WriteString(w, detail)
				case "/v1/device-p2p/stop":
					if r.Context().Err() != nil {
						t.Error("stop inherited cancellation")
					}
					stops.Add(1)
					io.WriteString(w, `{"code":200,"data":null,"msg":"ok"}`)
				default:
					switch mode {
					case "truncated":
						w.Header().Set("Content-Length", "1000")
						io.WriteString(w, `{"data":{"conId":"allocation-secret",`)
					case "malformed":
						io.WriteString(w, `{"data":{"conId":"allocation-secret","password":false}}`)
					case "oversized":
						io.WriteString(w, `{"data":{"conId":"allocation-secret","junk":"`+strings.Repeat("x", cloud.MaxBody))
					case "busy-with-id":
						io.WriteString(w, strings.Replace(online, `"code":200`, `"code":300403`, 1))
					case "http-error":
						w.WriteHeader(500)
						io.WriteString(w, online)
					case "read-cancel":
						io.WriteString(w, `{"data":{"conId":"allocation-secret",`)
						w.(http.Flusher).Flush()
						<-r.Context().Done()
					default:
						io.WriteString(w, online)
					}
				}
			})
			if mode == "read-cancel" {
				c.transport = &cancelReaderTransport{base: c.transport, cancel: cancel}
			}
			lease, err := c.Acquire(ctx, Device{Serial: "serial", Model: "S50 Pro"})
			if lease != nil || err == nil {
				t.Fatal("failed allocation returned a lease or no error")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Error("error leaked")
			}
			want := int32(1)
			if mode == "truncated" || mode == "oversized" || mode == "read-cancel" {
				want = 0
			}
			if stops.Load() != want {
				t.Fatalf("stops=%d", stops.Load())
			}
		})
	}
}

type cancelReaderTransport struct {
	base   Transport
	cancel context.CancelFunc
}

func (t *cancelReaderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err == nil && strings.HasSuffix(r.URL.Path, "online-link") {
		resp.Body = &cancelReader{ReadCloser: resp.Body, cancel: t.cancel}
	}
	return resp, err
}

type cancelReader struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelReader) Read(p []byte) (int, error) {
	n, e := r.ReadCloser.Read(p)
	if n > 0 {
		r.cancel()
	}
	return n, e
}
func TestBusyNeverRetried(t *testing.T) {
	for _, code := range []int{300403, 300404} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var calls atomic.Int32
			c := setup(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(400)
				fmt.Fprintf(w, `{"code":%d,"data":null,"msg":"token-secret"}`, code)
			})
			_, err := c.Acquire(context.Background(), Device{Serial: "serial", Model: "S50 Pro"})
			if !errors.Is(err, cloud.ErrBusy) || calls.Load() != 1 {
				t.Fatalf("busy: %v, calls=%d", err, calls.Load())
			}
		})
	}
}

func TestLeaseCloseOnceAndStopFailure(t *testing.T) {
	var stops atomic.Int32
	c := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "stop") {
			stops.Add(1)
			w.WriteHeader(503)
			io.WriteString(w, "token-secret")
			return
		}
		io.WriteString(w, online)
	})
	lease, err := c.Acquire(context.Background(), Device{Serial: "serial", Model: "S50 Pro"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() { done <- lease.Close() }()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; !errors.Is(err, cloud.ErrRejected) {
			t.Fatalf("stop: %v", err)
		}
	}
	if stops.Load() != 1 {
		t.Fatal("double stop")
	}
}
func TestRedirectAndUnauthorized(t *testing.T) {
	for _, status := range []int{302, 401, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			c := setup(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "https://untrusted.invalid")
				w.WriteHeader(status)
				io.WriteString(w, `{"code":200,"data":null,"msg":"token-secret"}`)
			})
			_, err := c.Acquire(context.Background(), Device{Serial: "serial", Model: "S50 Pro"})
			if err == nil || calls.Load() != 1 {
				t.Fatalf("status: %v calls %d", err, calls.Load())
			}
			if status == 401 && !errors.Is(err, cloud.ErrUnauthorized) {
				t.Fatal(err)
			}
		})
	}
}
func TestEnvironmentAndHTTPSOnly(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	for _, base := range []string{"http://example.invalid", "https://user:password@example.invalid", "https://example.invalid?token=secret", "https://example.invalid/path"} {
		if c, e := NewFromEnvironment(Config{BaseURL: base}); c != nil || e == nil || strings.Contains(e.Error(), "secret") {
			t.Fatal("invalid config")
		}
	}
	t.Setenv("SEESTAR_EMAIL", "")
	if c, e := NewFromEnvironment(Config{}); c != nil || e == nil {
		t.Fatal("missing email accepted")
	}
}

func TestStructuredLogsRedactDevice(t *testing.T) {
	var b strings.Builder
	logger := slog.New(slog.NewJSONHandler(&b, nil))
	logger.Info("test", "device", Device{Serial: "serial-secret", Model: "model-secret"})
	if strings.Contains(b.String(), "secret") {
		t.Fatal("structured log leaked device identity")
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestStopDeadlineIndependentOfCanceledAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {

		var stops int
		c, err := newAuthenticatedFixture(t, Config{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "stop") {
				stops++
				<-r.Context().Done()
				return nil, fmt.Errorf("token-secret: %w", r.Context().Err())
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(online))}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		lease, err := c.Acquire(ctx, Device{Serial: "serial", Model: "S50 Pro"})
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		before := time.Now()
		err = lease.Close()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(before) != 5*time.Second || stops != 1 {
			t.Fatalf("bounded stop: %v duration=%s count=%d", err, time.Since(before), stops)
		}
	})
}
func TestTransportErrorsAreSanitized(t *testing.T) {

	c, err := newAuthenticatedFixture(t, Config{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("failed %s Authorization=token-secret", r.URL)
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Acquire(context.Background(), Device{Serial: "serial-secret", Model: "model-secret"})
	if !errors.Is(err, cloud.ErrTransport) || strings.Contains(err.Error(), "secret") {
		t.Fatal("transport error leaked")
	}
}
func TestCanceledBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	c := setup(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Acquire(ctx, Device{Serial: "serial", Model: "S50 Pro"})
	if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("cancel: %v requests=%d", err, calls.Load())
	}
}

type panicBody struct{ read bool }

func (b *panicBody) Read(p []byte) (int, error) {
	if b.read {
		panic("body panic")
	}
	b.read = true
	return copy(p, online), nil
}
func (*panicBody) Close() error { return nil }
func TestReadPanicAfterIdentifierStillStops(t *testing.T) {

	var stops int
	c, err := newAuthenticatedFixture(t, Config{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "stop") {
			stops++
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":null,"msg":"ok"}`))}, nil
		}
		return &http.Response{StatusCode: 200, Body: &panicBody{}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := c.Acquire(context.Background(), Device{Serial: "serial", Model: "S50 Pro"})
	if lease != nil || !errors.Is(err, cloud.ErrInternal) || cloud.Disposition(err) != cloud.Fatal {
		t.Errorf("body panic outcome=%v/%v, want nil/fatal internal failure", lease, err)
	}

	if stops != 1 {
		t.Fatalf("lost allocation on read panic: stops=%d", stops)
	}
}
