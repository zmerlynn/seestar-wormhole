package proxy

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// Synthetic HTTP peer uses distinct IDs and checks live-wire call ordering.
// All time is synctest time; requests remain synthetic.
type r3Cloud struct {
	mu                    sync.Mutex
	down                  atomic.Bool
	live                  atomic.Int32
	requests, allocations int
	stops                 map[string]int
	bounds                []time.Duration
	violations            []string
	faultPath             string
	faultBody             string
	faultStatus           int
	bodyFault             bool
	stopStatus            int
	stopBody              string
}

func r3Response(status int, body string, fault bool) *http.Response {
	var reader io.Reader = strings.NewReader(body)
	if fault {
		reader = io.MultiReader(reader, r3ReadError{})
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(reader)}
}

type r3ReadError struct{}

func (r3ReadError) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (c *r3Cloud) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.requests++
	path := r.URL.Path
	status, body := 200, `{"code":200,"data":null}`
	switch path {
	case "/v1/user/login":
		body = `{"code":0,"data":{"accessToken":"synthetic-token"}}`
	case "/v1/user/current-profile":
		body = `{"code":0,"data":{"id":"synthetic-profile"}}`
	case "/v1/my-device":
		body = `{"code":200,"data":{"total":1,"items":[{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}]}}`
	case "/v1/my-device/detail":
		body = `{"code":200,"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`
	case "/v1/device-p2p/online-link":
		if !c.down.Load() {
			c.allocations++
			if c.live.Load() != 0 {
				c.violations = append(c.violations, "online-link while live")
			}
			body = fmt.Sprintf(`{"code":200,"data":{"conId":"r3-%d","deviceUid":"EXAMPLEUID0000000000","sdkLicenseKey":"fixture","username":"sim-user","password":"sim-pass"}}`, c.allocations)
		}
	case "/v1/device-p2p/stop":
		var decoded struct {
			ID string `json:"conId"`
		}
		if err := json.UnmarshalRead(r.Body, &decoded, json.MatchCaseInsensitiveNames(true)); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		if c.stops == nil {
			c.stops = map[string]int{}
		}
		c.stops[decoded.ID]++
		if c.live.Load() != 0 || decoded.ID != fmt.Sprintf("r3-%d", c.allocations) || c.stops[decoded.ID] != 1 {
			c.violations = append(c.violations, "overlap, stale or duplicate stop")
		}
		if c.stopStatus != 0 {
			status = c.stopStatus
		}
		if c.stopBody != "" {
			body = c.stopBody
		}
	}
	if path == "/v1/my-device" && r.URL.Query().Get("current") != "1" {
		c.mu.Unlock()
		return nil, errors.New("unexpected catalog page")
	}
	down := c.down.Load()
	if path == c.faultPath {
		status = c.faultStatus
		if c.faultBody != "" {
			body = c.faultBody
		}
	}
	fault := path == c.faultPath && c.bodyFault
	c.mu.Unlock()
	if down {
		if path == "/v1/device-p2p/stop" {
			began := time.Now()
			<-r.Context().Done()
			c.mu.Lock()
			c.bounds = append(c.bounds, time.Since(began))
			c.mu.Unlock()
			return nil, r.Context().Err()
		}
		return nil, syscall.ECONNREFUSED
	}
	return r3Response(status, body, fault), nil
}
func r3Client(t *testing.T, c *r3Cloud, account bool) *cloudhttp.Client {
	t.Helper()
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	if account {
		t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
		t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	}
	var client *cloudhttp.Client
	var err error
	if account {
		client, err = cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: c})
	} else {
		client, err = newFixtureClient(t, cloudhttp.Config{Transport: c})
	}
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type r3Wire struct {
	wireRunner
	c *r3Cloud
	t *testing.T
}

func (w r3Wire) Run(ctx context.Context, a <-chan iotc.Action, e func(iotc.Event) bool) {
	if n := w.c.live.Add(1); n != 1 {
		w.t.Errorf("live device sessions=%d, want 1", n)
	}
	defer w.c.live.Add(-1)
	w.wireRunner.Run(ctx, a, e)
}
func r3Session(t *testing.T, ctx context.Context, c *r3Cloud, client *cloudhttp.Client) *Session {
	t.Helper()
	s, err := start(ctx, Config{}, func(s *Session) sessionedge.Factory {
		return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
			trace := []string{}
			p := newSimulatedWire(t, &trace)
			p.outage = c.down.Load
			return r3Wire{p, c, t}
		}}
	})
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	return s
}
func TestCloudOutageSchedules(t *testing.T) {
	for _, schedule := range []struct {
		duration time.Duration
		signal   bool
	}{{211 * time.Second, false}, {12 * time.Hour, false}, {211 * time.Second, true}} {
		t.Run(fmt.Sprintf("%v/signal-%t", schedule.duration, schedule.signal), func(t *testing.T) {
			c := &r3Cloud{}
			synctest.Test(t, func(t *testing.T) {
				client := r3Client(t, c, false)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s := r3Session(t, ctx, c, client)
				defer s.Close()
				conn, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatal(err)
				}
				if err := exchange(conn.(*stream), 91, 0); err != nil {
					t.Fatal(err)
				}
				c.down.Store(true)
				<-time.After(schedule.duration)
				synctest.Wait()
				select {
				case <-s.Done():
					t.Fatalf("recoverable outage killed daemon: %v", s.Close())
				default:
				}
				c.mu.Lock()
				requests := c.requests
				if c.allocations != 1 || c.stops["r3-1"] != 1 || len(c.bounds) != 1 || c.bounds[0] != 5*time.Second || len(c.violations) != 0 {
					t.Errorf("outage allocations/stops/bounds/violations=%d/%v/%v/%v", c.allocations, c.stops, c.bounds, c.violations)
				}
				c.mu.Unlock()
				if c.live.Load() != 0 {
					t.Fatal("old wire did not join")
				}
				if schedule.duration >= time.Hour && (requests < 1500 || requests > 3000) {
					t.Errorf("12h requests=%d, want 1500..3000", requests)
				}
				conn.Close()
				if schedule.signal {
					before := time.Now()
					cancel()
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					if time.Since(before) > time.Second {
						t.Fatal("backoff cancellation exceeded 1s")
					}
				} else {
					c.down.Store(false)
					<-time.After(40 * time.Second)
					synctest.Wait()
					conn2, err := s.DialPort(ctx, 4700)
					if err != nil {
						t.Fatal(err)
					}
					if err := exchange(conn2.(*stream), 92, 2048); err != nil {
						t.Fatal(err)
					}
					conn2.Close()
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
				}
				synctest.Wait()
				c.mu.Lock()
				defer c.mu.Unlock()
				want := 1
				if !schedule.signal {
					want = 2
				}
				if c.allocations != want || len(c.stops) != want || c.live.Load() != 0 || len(c.violations) != 0 {
					t.Errorf("final allocations/stops/live/violations=%d/%v/%d/%v", c.allocations, c.stops, c.live.Load(), c.violations)
				}
				t.Logf("outage=%v cancel=%t requests=%d allocations=%d stops=%v bound=%v", schedule.duration, schedule.signal, requests, c.allocations, c.stops, c.bounds)
			})
		})
	}
}

// The call matrix includes profile and catalog paths absent from this call set.
func TestEveryCloudCall(t *testing.T) {
	for _, path := range []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device", "/v1/my-device/detail", "/v1/device-p2p/online-link", "/v1/device-p2p/stop"} {
		for _, status := range []int{200, 401, 403, 409, 429, 500, 502, 503, 504} {
			for _, bodyFault := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/bodyFault-%t", path, status, bodyFault), func(t *testing.T) {
					c := &r3Cloud{faultPath: path, faultStatus: status, bodyFault: bodyFault}
					synctest.Test(t, func(t *testing.T) {
						client := r3Client(t, c, strings.HasPrefix(path, "/v1/user/"))
						defer client.Close()
						var err error
						d := cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}
						switch path {
						case "/v1/user/login", "/v1/user/current-profile":
							err = client.Login(t.Context())
						case "/v1/my-device":
							bounded, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
							defer cancel()
							_, err = selectDevice(bounded, client, cloudhttp.Device{}, nil, rand.NewPCG(1, 2))
						case "/v1/my-device/detail":
							var l sessionedge.Lease
							l, err = (&allocator{client, d}).Allocate(t.Context())
							if l != nil {
								defer l.Stop(t.Context())
							}
						case "/v1/device-p2p/online-link":
							var l sessionedge.Lease
							l, err = (&allocator{client, d}).Allocate(t.Context())
							if l != nil {
								defer l.Stop(t.Context())
							}
						case "/v1/device-p2p/stop":
							l, e := (&allocator{client, d}).Allocate(t.Context())
							if e != nil {
								t.Fatal(e)
							}
							err = l.Stop(t.Context())
						}
						temporary := (status == 429 || status == 502 || status == 503 || status == 504) && path != "/v1/device-p2p/online-link" && path != "/v1/device-p2p/stop"
						if temporary {
							if !(retryableCloud(err) || errors.Is(err, session.ErrTransport) || path == "/v1/my-device" && errors.Is(err, context.DeadlineExceeded)) {
								t.Errorf("temporary status %d -> %v, want retryable failure or canceled selection", status, err)
							}
						} else if status != 200 {
							if retryableCloud(err) || errors.Is(err, session.ErrTransport) || err == nil {
								t.Errorf("received status %d -> %v, want blocking", status, err)
							}
						} else if !bodyFault && err != nil {
							t.Errorf("complete successful response=%v, want nil", err)
						} else if bodyFault && path != "/v1/my-device" && !(retryableCloud(err) || errors.Is(err, session.ErrTransport) || errors.Is(err, session.ErrReleaseAbandoned)) {
							t.Errorf("successful headers + body I/O=%v, want recoverable", err)
						}
					})
				})
			}
		}
	}
}

// A retryable allocation must not weaken the denial from its own owed stop.
func TestPartialCleanupDenialPolicy(t *testing.T) {
	for _, stop := range []struct {
		name   string
		status int
		body   string
		block  bool
	}{
		{"accepted", 200, "", false}, {"unauthorized", 401, "", true}, {"forbidden", 403, "", true}, {"http-rejected", 409, "", true}, {"vendor-rejected", 200, `{"code":300405,"data":null}`, true}, {"malformed", 200, `{`, true},
	} {
		t.Run(stop.name, func(t *testing.T) {
			c := &r3Cloud{faultPath: "/v1/device-p2p/online-link", faultStatus: 200, bodyFault: true, stopStatus: stop.status, stopBody: stop.body}
			client := r3Client(t, c, false)
			defer client.Close()
			_, err := (&allocator{client, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}).Allocate(t.Context())
			if stop.block && !errors.Is(err, session.ErrStop) {
				t.Errorf("partial allocation body I/O + stop %s=%v, want ErrStop", stop.name, err)
			}
			if !stop.block && (!errors.Is(err, session.ErrTransport) || errors.Is(err, session.ErrStop)) {
				t.Errorf("accepted cleanup=%v, want transport recovery", err)
			}
			if c.allocations != 1 || c.stops["r3-1"] != 1 {
				t.Errorf("allocation/stops=%d/%v, want 1/single", c.allocations, c.stops)
			}
		})
	}
}
func TestPartialCleanupDenialDaemon(t *testing.T) {
	for _, status := range []int{401, 403, 409} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := &r3Cloud{faultPath: "/v1/device-p2p/online-link", faultStatus: 200, bodyFault: true, stopStatus: status}
			synctest.Test(t, func(t *testing.T) {
				client := r3Client(t, c, false)
				s := r3Session(t, context.Background(), c, client)
				defer s.Close()
				<-time.After(2 * time.Minute)
				synctest.Wait()
				c.mu.Lock()
				defer c.mu.Unlock()
				if c.allocations != 1 || len(c.stops) != 1 {
					t.Errorf("received stop %d after partial allocation: allocations=%d stops=%v, want 1/one stop", status, c.allocations, c.stops)
				}
			})
		})
	}
}

// A complete offline outcome carries no incidental transport cause. The production
// vendor matrix only tests offline outcomes together with reader I/O failure.
func TestCompleteOffline(t *testing.T) {
	for _, path := range []string{"/v1/my-device/detail", "/v1/device-p2p/online-link", "/v1/device-p2p/stop"} {
		t.Run(path, func(t *testing.T) {
			tr := &r3Cloud{faultPath: path, faultStatus: 200, faultBody: `{"code":300406,"data":{"conId":"r3-1","deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"synthetic-wifi"}}`}
			client := r3Client(t, tr, false)
			defer client.Close()
			lease, err := (&allocator{client, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}).Allocate(t.Context())
			if path == "/v1/device-p2p/stop" {
				if err != nil {
					t.Fatal(err)
				}
				err = lease.Stop(t.Context())
				if !errors.Is(err, session.ErrReleaseAbandoned) {
					t.Errorf("complete offline stop=%v, want abandonment", err)
				}
			} else if !errors.Is(err, session.ErrTransport) || errors.Is(err, session.ErrUncertain) {
				t.Errorf("complete offline %s=%v, want retryable transport", path, err)
			}
		})
	}
}
