package proxy

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// Account mode exercises fresh startup login as well as established wire loss.
// Only the existing simulated binary wire peer is reused; this cloud and oracle
// have independent bodies, schedules, identifiers and accounting.
type r4Cloud struct {
	offline               atomic.Bool
	live                  atomic.Int32
	mu                    sync.Mutex
	allocations, requests int
	stops                 map[string]int
	stopBounds            []time.Duration
	violation             []string
}

func r4Response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}
}
func (c *r4Cloud) RoundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.requests++
	path := req.URL.Path
	if path == "/v1/device-p2p/stop" {
		var b struct {
			ID string `json:"conId"`
		}
		if err := json.UnmarshalRead(req.Body, &b, json.MatchCaseInsensitiveNames(true)); err != nil {
			panic(err)
		}
		c.stops[b.ID]++
		if c.stops[b.ID] != 1 || b.ID != fmt.Sprintf("r4-%d", c.allocations) || c.live.Load() != 0 {
			c.violation = append(c.violation, "duplicate/stale/live stop "+b.ID)
		}
	}
	off := c.offline.Load()
	if !off && path == "/v1/device-p2p/online-link" {
		if c.live.Load() != 0 {
			c.violation = append(c.violation, "allocation while old wire live")
		}
		c.allocations++
	}
	n := c.allocations
	c.mu.Unlock()
	if off {
		start := time.Now()
		<-req.Context().Done()
		if path == "/v1/device-p2p/stop" {
			c.mu.Lock()
			c.stopBounds = append(c.stopBounds, time.Since(start))
			c.mu.Unlock()
		}
		return nil, req.Context().Err()
	}
	switch path {
	case "/v1/user/login":
		return r4Response(`{"code":0,"data":{"accessToken":"synthetic-r4"}}`), nil
	case "/v1/user/current-profile", "/v1/device-p2p/stop":
		return r4Response(`{"code":200,"data":null}`), nil
	case "/v1/my-device":
		if req.URL.Query().Get("current") != "1" {
			return nil, errors.New("unexpected catalog page")
		}
		return r4Response(`{"code":200,"data":{"items":[{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}],"total":1}}`), nil
	case "/v1/my-device/detail":
		return r4Response(`{"code":200,"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`), nil
	case "/v1/device-p2p/online-link":
		return r4Response(fmt.Sprintf(`{"code":200,"data":{"conId":"r4-%d","deviceUid":"EXAMPLEUID0000000000","sdkLicenseKey":"fixture","username":"sim-user","password":"sim-pass"}}`, n)), nil
	}
	return nil, errors.New("unexpected synthetic endpoint")
}

type r4Wire struct {
	wireRunner
	c *r4Cloud
	t *testing.T
}

func (w r4Wire) Run(ctx context.Context, a <-chan iotc.Action, emit func(iotc.Event) bool) {
	if n := w.c.live.Add(1); n != 1 {
		w.t.Errorf("live sessions=%d want 1", n)
	}
	defer w.c.live.Add(-1)
	w.wireRunner.Run(ctx, a, emit)
}
func r4Session(t *testing.T, ctx context.Context, c *r4Cloud) *Session {
	client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: c, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s, err := start(ctx, Config{}, func(s *Session) sessionedge.Factory {
		return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
			trace := []string{}
			p := newSimulatedWire(t, &trace)
			p.outage = c.offline.Load
			return r4Wire{p, c, t}
		}}
	})
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	return s
}
func TestBackoffRecoversAfterIndependentOutage(t *testing.T) {
	for _, duration := range []time.Duration{211 * time.Second, 12 * time.Hour} {
		t.Run(duration.String(), func(t *testing.T) {
			t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
			t.Setenv("SEESTAR_PASSWORD", "fixture-password")
			synctest.Test(t, func(t *testing.T) {
				c := &r4Cloud{stops: map[string]int{}}
				s := r4Session(t, t.Context(), c)
				defer s.Close()
				conn, err := s.DialPort(t.Context(), 4700)
				if err != nil {
					t.Fatal(err)
				}
				if err := exchange(conn.(*stream), 84, 32); err != nil {
					t.Fatal(err)
				}
				c.offline.Store(true)
				<-time.After(duration)
				synctest.Wait()
				select {
				case <-s.Done():
					t.Fatal("daemon exited in recoverable outage")
				default:
				}
				c.mu.Lock()
				requests := c.requests
				if c.stops["r4-1"] != 1 || len(c.stopBounds) != 1 || c.stopBounds[0] != 5*time.Second {
					t.Errorf("stops/bounds=%v/%v want original once/5s", c.stops, c.stopBounds)
				}
				c.mu.Unlock()
				if duration > time.Hour && (requests < 600 || requests > 3000) {
					t.Errorf("12h requests=%d want 600..3000", requests)
				}
				if c.live.Load() != 0 {
					t.Fatal("old wire survived outage")
				}
				conn.Close()
				c.offline.Store(false)
				<-time.After(90 * time.Second)
				synctest.Wait()
				conn, err = s.DialPort(t.Context(), 4700)
				if err != nil {
					t.Fatal(err)
				}
				if err := exchange(conn.(*stream), 85, 4096); err != nil {
					t.Fatal(err)
				}
				conn.Close()
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				c.mu.Lock()
				defer c.mu.Unlock()
				if c.allocations != 2 || c.stops["r4-2"] != 1 || len(c.stops) != 2 || len(c.violation) != 0 || c.live.Load() != 0 {
					t.Errorf("allocations/stops/violations/live=%d/%v/%v/%d", c.allocations, c.stops, c.violation, c.live.Load())
				}
				t.Logf("outage=%v requests=%d allocations=%d stops=%v bounds=%v", duration, requests, c.allocations, c.stops, c.stopBounds)
			})
		})
	}
}
func TestBackoffCancellation(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		c := &r4Cloud{stops: map[string]int{}}
		s := r4Session(t, ctx, c)
		defer s.Close()
		conn, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		c.offline.Store(true)
		for n := 0; n < 20000; n++ {
			<-time.After(10 * time.Millisecond)
			synctest.Wait()
			c.mu.Lock()
			released := len(c.stopBounds) == 1
			before := c.requests
			c.mu.Unlock()
			if !released {
				continue
			}
			start := time.Now()
			cancel()
			err := s.Close()
			synctest.Wait()
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if time.Since(start) > 5*time.Second || c.requests != before || c.allocations != 1 || c.stops["r4-1"] != 1 || c.live.Load() != 0 {
				t.Errorf("cancellation=%v requests=%d->%d allocs=%d stops=%v", time.Since(start), before, c.requests, c.allocations, c.stops)
			}
			t.Logf("backoff cancellation=%v one attempted stop", time.Since(start))
			return
		}
		t.Fatal("never observed release/backoff")
	})
}

// Profile 401 is allowed to start renewal, but a recovered internal panic must
// not disappear merely because the response also supplies that retry trigger.
type r4ProfileFault struct {
	faults           int
	mode             string
	logins, profiles int
}
type r4ProfileBody struct {
	io.ReadCloser
	mode string
}

func (b r4ProfileBody) Close() error {
	if b.mode == "panic" {
		panic("synthetic close panic")
	}
	return b.ReadCloser.Close()
}
func (c *r4ProfileFault) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/v1/user/login" {
		c.logins++
		return r4Response(`{"code":0,"data":{"accessToken":"r4-token"}}`), nil
	}
	c.profiles++
	if c.profiles == 1 {
		c.faults++
		r := r4Response(`{"code":0,"data":null}`)
		r.StatusCode = 401
		r.Body = r4ProfileBody{r.Body, c.mode}
		return r, nil
	}
	return r4Response(`{"code":0,"data":null}`), nil
}
func TestProfileRenewalPreservesFatal(t *testing.T) {
	for _, mode := range []string{"control", "panic"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
			t.Setenv("SEESTAR_PASSWORD", "fixture-password")
			tr := &r4ProfileFault{mode: mode}
			c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr, StateDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			err = c.Login(t.Context())
			if mode == "control" && err != nil {
				t.Fatal(err)
			}
			if mode == "panic" && (!errors.Is(err, cloud.ErrInternal) || cloud.Disposition(err) != cloud.Fatal) {
				t.Errorf("profile 401 + Close panic=%v disposition=%d logins/profiles=%d/%d want retained fatal", err, cloud.Disposition(err), tr.logins, tr.profiles)
			}
			t.Logf("mode=%s error=%v disposition=%d logins/profiles=%d/%d", mode, err, cloud.Disposition(err), tr.logins, tr.profiles)
		})
	}
}

// Same timeout at every real HTTP entry point, with a complete successful JSON
// body or an incomplete prefix. Parser absence must not invent a fatal outcome.
type r4TimeoutReader struct{}

func (r4TimeoutReader) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }

type r4BodyTimeoutCloud struct {
	r4Cloud
	path       string
	incomplete bool
}

func (c *r4BodyTimeoutCloud) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.r4Cloud.RoundTrip(req)
	if req.URL.Path != c.path || err != nil {
		return resp, err
	}
	if c.incomplete {
		resp.Body = io.NopCloser(strings.NewReader(`{"code":`))
	}
	resp.Body = io.NopCloser(io.MultiReader(resp.Body, r4TimeoutReader{}))
	return resp, nil
}
func TestHTTP200BodyTimeoutIsRecoverable(t *testing.T) {
	for _, path := range []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device", "/v1/my-device/detail", "/v1/device-p2p/online-link", "/v1/device-p2p/stop"} {
		for _, incomplete := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/incomplete=%v", path, incomplete), func(t *testing.T) {
				t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
				t.Setenv("SEESTAR_PASSWORD", "fixture-password")
				tr := &r4BodyTimeoutCloud{r4Cloud: r4Cloud{stops: map[string]int{}}, path: path, incomplete: incomplete}
				c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr, StateDir: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				// Catalog has its own DTO; use a separate synthetic wrapper below.
				var result error
				switch path {
				case "/v1/user/login", "/v1/user/current-profile":
					result = c.Login(t.Context())
				case "/v1/my-device":
					_, result = c.SelectDevice(t.Context(), cloudhttp.Device{})
				case "/v1/my-device/detail":
					_, result = c.Detail(t.Context(), cloudhttp.Device{Serial: "sim", Model: "S50 Pro"})
				case "/v1/device-p2p/online-link":
					_, result = c.Acquire(t.Context(), cloudhttp.Device{Serial: "sim", Model: "S50 Pro"})
				case "/v1/device-p2p/stop":
					l, e := c.Acquire(t.Context(), cloudhttp.Device{Serial: "sim", Model: "S50 Pro"})
					if e != nil {
						t.Fatal(e)
					}
					result = cloud.ReleaseOutcome(l.Close())
				}
				want := cloud.Retryable
				if path == "/v1/device-p2p/stop" {
					want = cloud.OwedRelease
				}
				if got := cloud.Disposition(result); got != want {
					t.Errorf("HTTP200 body timeout=%v rank=%d want %d", result, got, want)
				}
			})
		}
	}
}
