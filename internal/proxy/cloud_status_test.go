package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// Independent literal bodies deliberately finish JSON before failing the read.
// The received HTTP status must keep the same policy as a completed body.
type statusFaultCloud struct {
	path, mode                     string
	body                           string
	status                         int
	faults, details, online, stops atomic.Int32
	outage                         atomic.Bool
}

func (c *statusFaultCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{"code":200,"data":null}`
	switch r.URL.Path {
	case "/v1/user/login":
		body = `{"code":0,"data":{"accessToken":"fixture-token"}}`
	case "/v1/my-device/detail":
		c.details.Add(1)
		body = `{"code":200,"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`
	case "/v1/device-p2p/online-link":
		c.online.Add(1)
		body = `{"code":200,"data":{"conId":"status-fixture","deviceUid":"EXAMPLEUID0000000000","sdkLicenseKey":"fixture","username":"sim-user","password":"sim-pass"}}`
	case "/v1/device-p2p/stop":
		c.stops.Add(1)
	}
	status := 200
	var reader io.ReadCloser = io.NopCloser(strings.NewReader(body))
	if r.URL.Path == c.path {
		c.faults.Add(1)
		status = c.status
		if c.body != "" {
			body = c.body
			reader = io.NopCloser(strings.NewReader(body))
		}
		switch c.mode {
		case "timeout":
			reader = io.NopCloser(io.MultiReader(strings.NewReader(body), allocationReadFailure{}))
		case "read-error":
			reader = io.NopCloser(io.MultiReader(strings.NewReader(body), statusReadFailure{}))
		case "nil-body":
			reader = nil
		}
	}
	return &http.Response{StatusCode: status, Body: reader}, nil
}

type statusReadFailure struct{}

func (statusReadFailure) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func statusClient(t *testing.T, tr *statusFaultCloud) *cloudhttp.Client {
	t.Helper()
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	if tr.path == "/v1/user/login" {
		t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
		t.Setenv("SEESTAR_PASSWORD", "fixture-password")

	}
	cfg := cloudhttp.Config{Transport: tr}
	var c *cloudhttp.Client
	var err error
	if tr.path == "/v1/user/login" {
		c, err = cloudhttp.NewFromEnvironment(cfg)
	} else if tr.path == "/v1/device-p2p/online-link" && tr.status == 401 {
		c, err = newExpiredAllocationFixtureClient(t, cfg)
	} else {
		c, err = newFixtureClient(t, cfg)
	}
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCloudCallStatusBodyMatrix(t *testing.T) {
	for _, path := range []string{"/v1/user/login", "/v1/my-device/detail", "/v1/device-p2p/online-link", "/v1/device-p2p/stop"} {
		for _, status := range []int{200, 401, 403, 409, 503} {
			for _, mode := range []string{"complete", "timeout", "read-error", "nil-body"} {
				t.Run(fmt.Sprintf("%s/%d/%s", path, status, mode), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						tr := &statusFaultCloud{path: path, status: status, mode: mode}
						c := statusClient(t, tr)
						defer c.Close()
						var err error
						switch path {
						case "/v1/user/login":
							err = c.Login(t.Context())
						case "/v1/my-device/detail":
							_, err = c.Detail(t.Context(), cloudhttp.Device{Serial: "sim", Model: "S50 Pro"})
						default:
							l, acquireErr := c.Acquire(t.Context(), cloudhttp.Device{Serial: "sim", Model: "S50 Pro"})
							err = acquireErr
							if l != nil {
								err = l.Close()
							}
						}
						if status != 200 {
							want := cloud.ErrRejected
							if path != "/v1/user/login" && (status == 401 || status == 403) {
								want = cloud.ErrUnauthorized
							}
							if !errors.Is(err, want) {
								t.Errorf("status error=%v, want %v", err, want)
							}
							if path == "/v1/user/login" {
								if again := c.Login(t.Context()); !errors.Is(again, want) || tr.faults.Load() != 1 {
									t.Errorf("second login=%v, requests=%d, want rejection/1", again, tr.faults.Load())
								}
							}
						} else if mode != "complete" && !errors.Is(err, cloud.ErrTransport) {
							t.Errorf("body error=%v, want transport", err)
						}
						if status != 200 && retryableCloud(err) {
							t.Errorf("HTTP %d with %s is retryable: %v", status, mode, err)
						}
					})
				})
			}
		}
	}
}

func TestBusyAndRejectedCloudCallsPolicy(t *testing.T) {
	for _, path := range []string{"/v1/user/login", "/v1/my-device/detail", "/v1/device-p2p/online-link", "/v1/device-p2p/stop"} {
		for _, failure := range []struct{ status, code int }{{401, 0}, {403, 0}, {409, 0}, {503, 0}, {200, 300403}, {200, 300404}, {200, 300405}} {
			status := failure.status
			if path == "/v1/user/login" && failure.code != 0 {
				continue
			} // Login consumes HTTP status, not these remote vendor codes.
			for _, mode := range []string{"complete", "timeout", "read-error", "nil-body"} {
				if failure.code != 0 && mode == "nil-body" {
					continue
				} // No received vendor code without a decoded body.
				name := fmt.Sprintf("%s/%d/%s", path, status, mode)
				if failure.code != 0 {
					name += fmt.Sprintf("/vendor-%d", failure.code)
				}
				t.Run(name, func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						tr := &statusFaultCloud{path: path, status: status, mode: mode}
						if failure.code != 0 {
							tr.body = fmt.Sprintf(`{"code":%d,"data":{"conId":"vendor-fixture","deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`, failure.code)
						}
						c := statusClient(t, tr)
						s, err := start(context.Background(), Config{}, func(s *Session) sessionedge.Factory {
							return &factory{s: s, cloud: c, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
								trace := []string{}
								p := newSimulatedWire(t, &trace)
								p.outage = tr.outage.Load
								return p
							}}
						})
						if err != nil {
							c.Close()
							t.Fatal(err)
						}
						defer s.Close()
						if path == "/v1/device-p2p/stop" {
							conn, err := s.DialPort(t.Context(), 4700)
							if err != nil {
								t.Fatal(err)
							}
							defer conn.Close()
							if err := exchange(conn.(*stream), 71, 0); err != nil {
								t.Fatal(err)
							}
							tr.outage.Store(true)
						}
						<-time.After(2 * time.Minute)
						synctest.Wait()
						ctx, cancel := context.WithTimeout(t.Context(), time.Second)
						defer cancel()
						_, err = s.DialPort(ctx, 4700)
						want := session.ErrStop
						if (failure.code == 300403 || failure.code == 300404) && path != "/v1/device-p2p/stop" {
							want = session.ErrBusy
						}
						if !errors.Is(err, want) {
							t.Errorf("blocked client error=%v, want %v after joined cleanup", err, want)
						}
						wantOnline, wantDetails, wantStops := int32(0), int32(1), int32(0)
						if path == "/v1/user/login" {
							wantDetails = 0
						}
						if path == "/v1/device-p2p/online-link" || path == "/v1/device-p2p/stop" {
							wantOnline = 1
							wantStops = 1
						}
						if path == "/v1/device-p2p/online-link" && (mode == "nil-body" || status == 401) {
							wantStops = 0
						}
						if want == session.ErrBusy {
							// Known vendor busy keeps its classification even when a body
							// read fails after decoding it. Ambient retry is now indefinite;
							// every partial lease must still stop before replacement.
							faults := tr.faults.Load()
							if faults < 2 || tr.details.Load() != faults || tr.online.Load() != faults*wantOnline || tr.stops.Load() != faults*wantStops {
								t.Errorf("busy fault/detail/online/stop=%d/%d/%d/%d, want ongoing retries with matched cleanup", faults, tr.details.Load(), tr.online.Load(), tr.stops.Load())
							}
							if err := s.Close(); err != nil {
								t.Errorf("handled ambient busy shutdown=%v, want nil", err)
							}
						} else if tr.faults.Load() != 1 || tr.details.Load() != wantDetails || tr.online.Load() != wantOnline || tr.stops.Load() != wantStops {
							t.Errorf("fault/detail/online/stop=%d/%d/%d/%d, want 1/%d/%d/%d", tr.faults.Load(), tr.details.Load(), tr.online.Load(), tr.stops.Load(), wantDetails, wantOnline, wantStops)
						}
					})
				})
			}
		}
	}
}

func TestCloudLeaseStatusBodyPolicy(t *testing.T) {
	for _, status := range []int{200, 401, 403, 409, 503} {
		for _, mode := range []string{"timeout", "read-error", "nil-body"} {
			t.Run(fmt.Sprintf("%d/%s", status, mode), func(t *testing.T) {
				tr := &statusFaultCloud{path: "/v1/device-p2p/stop", status: status, mode: mode}
				c := statusClient(t, tr)
				defer c.Close()
				a := allocator{c, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
				l, err := a.Allocate(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				want := session.ErrStop
				if status == 200 {
					want = session.ErrReleaseAbandoned
				}
				for range 2 {
					if err := l.Stop(t.Context()); !errors.Is(err, want) {
						t.Errorf("stop error=%v, want %v", err, want)
					}
				}
				if tr.online.Load() != 1 || tr.stops.Load() != 1 {
					t.Errorf("online/stop=%d/%d, want 1/1", tr.online.Load(), tr.stops.Load())
				}
			})
		}
	}
}

func TestKnownVendorStatusBodyFailures(t *testing.T) {
	for _, path := range []string{"/v1/my-device/detail", "/v1/device-p2p/online-link", "/v1/device-p2p/stop"} {
		for _, status := range []struct {
			code           int
			cause, outcome error
		}{
			{300403, cloud.ErrBusy, session.ErrBusy},
			{300404, cloud.ErrBusy, session.ErrBusy},
			{300405, cloud.ErrRejected, session.ErrUncertain},
			{300406, cloud.ErrOffline, session.ErrTransport},
		} {
			for _, mode := range []string{"timeout", "read-error"} {
				t.Run(fmt.Sprintf("%s/%d/%s", path, status.code, mode), func(t *testing.T) {
					body := fmt.Sprintf(`{"code":%d,"data":{"conId":"vendor-fixture","deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`, status.code)
					tr := &statusFaultCloud{path: path, status: 200, mode: mode, body: body}
					c := statusClient(t, tr)
					defer c.Close()
					var err error
					switch path {
					case "/v1/my-device/detail":
						_, err = c.Detail(t.Context(), cloudhttp.Device{Serial: "sim", Model: "S50 Pro"})
					default:
						l, acquireErr := c.Acquire(t.Context(), cloudhttp.Device{Serial: "sim", Model: "S50 Pro"})
						err = acquireErr
						if l != nil {
							err = l.Close()
						}
					}
					if !errors.Is(err, status.cause) || !errors.Is(err, cloud.ErrTransport) {
						t.Errorf("known status/body error=%v, want %v and transport", err, status.cause)
					}
					if got := cloudOutcome(err); got != status.outcome {
						t.Errorf("outcome=%v, want %v", got, status.outcome)
					}
				})
			}
		}
	}
}
