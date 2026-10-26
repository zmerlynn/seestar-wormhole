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
	logins                         atomic.Int32
	outage                         atomic.Bool
	healthy                        atomic.Bool
}

func (c *statusFaultCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{"code":200,"data":null}`
	switch r.URL.Path {
	case "/v1/user/login":
		c.logins.Add(1)
		body = `{"code":0,"data":{"accessToken":"fixture-token"}}`
	case "/v1/my-device":
		body = `{"code":200,"data":{"items":[{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}],"total":1}}`
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
	if r.URL.Path == c.path && !c.healthy.Load() {
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

	account := tr.path == "/v1/user/login" || tr.path == "/v1/user/current-profile"
	if account {
		t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
		t.Setenv("SEESTAR_PASSWORD", "fixture-password")

	}
	cfg := cloudhttp.Config{Transport: tr}
	var c *cloudhttp.Client
	var err error
	if account {
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
	for _, path := range []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device", "/v1/my-device/detail", "/v1/device-p2p/online-link", "/v1/device-p2p/stop"} {
		for _, status := range []int{200, 400, 401, 403, 404, 408, 409, 429, 500, 501, 502, 503, 504, 505} {
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
						case "/v1/user/current-profile":
							err = c.CheckProfile(t.Context())
						case "/v1/my-device":
							_, err = c.SelectDevice(t.Context(), cloudhttp.Device{})
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
							temporary := (status == 429 || status == 502 || status == 503 || status == 504) && path != "/v1/device-p2p/online-link" && path != "/v1/device-p2p/stop"
							if path != "/v1/user/login" && (status == 401 || status == 403) {
								want = cloud.ErrUnauthorized
							}
							if temporary {
								want = cloud.ErrTransport
							}
							if !errors.Is(err, want) {
								t.Errorf("status error=%v, want %v", err, want)
							}
							if path == "/v1/user/login" && !temporary {
								if again := c.Login(t.Context()); !errors.Is(again, want) || tr.faults.Load() != 1 {
									t.Errorf("second login=%v, requests=%d, want rejection/1", again, tr.faults.Load())
								}
							}
							if got := retryableCloud(err); got != temporary {
								t.Errorf("HTTP %d with %s retryable=%v, want %v", status, mode, got, temporary)
							}
						} else if mode != "complete" && !errors.Is(err, cloud.ErrTransport) {
							t.Errorf("body error=%v, want transport", err)
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
			if status == 503 && (path == "/v1/user/login" || path == "/v1/my-device/detail") {
				continue // Temporary failures on non-allocating requests recover below.
			}
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
						if path == "/v1/device-p2p/stop" && status != 401 {
							// A stop response is reduced to release_failed after joining.
							// Recover that ownership block; the synthetic wire stays down.
							if !errors.Is(err, session.ErrTransport) && !errors.Is(err, session.ErrStop) {
								t.Errorf("recovering client error=%v, want closed unavailable client", err)
							}
							if tr.online.Load() < 2 || tr.details.Load() != tr.online.Load() || tr.stops.Load() < 1 {
								t.Errorf("stop recovery detail/online/stop=%d/%d/%d", tr.details.Load(), tr.online.Load(), tr.stops.Load())
							}
							return
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

func TestTemporaryCloudStartupRecovery(t *testing.T) {
	for _, path := range []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device"} {
		for _, status := range []int{429, 502, 503, 504} {
			t.Run(fmt.Sprintf("%s/%d", path, status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					tr := &statusFaultCloud{path: path, status: status, body: "<html>temporary service error</html>"}
					c := statusClient(t, tr)
					defer c.Close()
					result := make(chan error, 1)
					go func() {
						d, err := selectDevice(t.Context(), c, cloudhttp.Device{}, nil, zeroRetryRNG{})
						if err == nil && d != (cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}) {
							err = errors.New("wrong selected fixture")
						}
						result <- err
					}()
					<-time.After(2 * time.Minute)
					synctest.Wait()
					if path == "/v1/user/current-profile" {
						// As with profile I/O loss, retry uses the installed token.
						select {
						case err := <-result:
							if err != nil || tr.faults.Load() != 1 || tr.logins.Load() != 1 {
								t.Fatalf("profile recovery=%v, requests/logins=%d/%d", err, tr.faults.Load(), tr.logins.Load())
							}
						default:
							t.Fatal("temporary profile failure stranded selection")
						}
						tr.healthy.Store(true)
						if err := c.CheckProfile(t.Context()); err != nil {
							t.Fatalf("profile after service recovery: %v", err)
						}
						if tr.logins.Load() != 1 {
							t.Fatal("profile recovery requested credentials again")
						}
						return
					}
					select {
					case err := <-result:
						t.Fatalf("selection ended during temporary outage: %v", err)
					default:
					}
					if n := tr.faults.Load(); n != 12 {
						t.Fatalf("temporary requests=%d, want 12 with existing zero-jitter backoff", n)
					}
					tr.healthy.Store(true)
					select {
					case err := <-result:
						if err != nil {
							t.Fatalf("selection after service recovery: %v", err)
						}
					case <-time.After(31 * time.Second):
						t.Fatal("selection did not recover within capped backoff")
					}
					if tr.online.Load() != 0 || tr.stops.Load() != 0 {
						t.Fatal("selection created ownership")
					}
				})
			})
		}
	}
}

func TestTemporaryCloudOwnerRecovery(t *testing.T) {
	for _, path := range []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device/detail"} {
		for _, status := range []int{429, 502, 503, 504} {
			for _, ready := range []bool{false, true} {
				if ready && path != "/v1/my-device/detail" {
					continue // Installed account authorization is reused after readiness.
				}
				t.Run(fmt.Sprintf("%s/%d/ready=%v", path, status, ready), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						tr := &statusFaultCloud{path: path, status: status, body: "<html>temporary service error</html>"}
						tr.healthy.Store(ready)
						c := statusClient(t, tr)
						defer c.Close()
						s, err := start(t.Context(), Config{}, func(s *Session) sessionedge.Factory {
							return &factory{s: s, cloud: c, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
								trace := []string{}
								p := newSimulatedWire(t, &trace)
								p.outage = tr.outage.Load
								return p
							}}
						})
						if err != nil {
							t.Fatal(err)
						}
						defer s.Close()
						if ready {
							conn, err := s.DialPort(t.Context(), 4700)
							if err != nil {
								t.Fatal(err)
							}
							if err := exchange(conn.(*stream), 81, 0); err != nil {
								t.Fatal(err)
							}
							tr.healthy.Store(false)
							tr.outage.Store(true)
							defer conn.Close()
						}
						<-time.After(2 * time.Minute)
						synctest.Wait()
						profile := path == "/v1/user/current-profile"
						if n := tr.faults.Load(); !profile && (n < 3 || n > 20) {
							t.Fatalf("temporary requests=%d, want capped ongoing backoff", n)
						}
						wantAllocations, wantStops := int32(0), int32(0)
						if ready {
							wantAllocations, wantStops = 1, 1
						}
						if profile {
							wantAllocations = 1
							if tr.faults.Load() != 1 || tr.logins.Load() != 1 {
								t.Fatalf("profile requests/logins=%d/%d, want 1/1", tr.faults.Load(), tr.logins.Load())
							}
						}
						if tr.online.Load() != wantAllocations || tr.stops.Load() != wantStops {
							t.Fatalf("outage allocation/stop=%d/%d, want %d/%d", tr.online.Load(), tr.stops.Load(), wantAllocations, wantStops)
						}
						tr.healthy.Store(true)
						tr.outage.Store(false)
						if profile {
							if err := c.CheckProfile(t.Context()); err != nil {
								t.Fatalf("profile after service recovery: %v", err)
							}
							if tr.logins.Load() != 1 {
								t.Fatal("profile recovery requested credentials again")
							}
						}
						<-time.After(31 * time.Second)
						synctest.Wait()
						ctx, cancel := context.WithTimeout(t.Context(), time.Second)
						defer cancel()
						conn, err := s.DialPort(ctx, 4700)
						if err != nil {
							t.Fatalf("same owner after service recovery: %v", err)
						}
						defer conn.Close()
						if err := exchange(conn.(*stream), 82, 1024); err != nil {
							t.Fatal(err)
						}
						if err := s.Close(); err != nil {
							t.Fatal(err)
						}
						wantFinal := wantStops + 1
						if tr.online.Load() != wantFinal || tr.stops.Load() != wantFinal {
							t.Fatalf("final allocation/stop=%d/%d, want %d/%d", tr.online.Load(), tr.stops.Load(), wantFinal, wantFinal)
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
