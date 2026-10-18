package proxy

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// Independent cloud ledger uses the simulated binary peer only beneath the real
// runtime, cloud, and session edges.
type r5Cloud struct {
	offline             atomic.Bool
	live                atomic.Int32
	mu                  sync.Mutex
	requests, allocated int
	stops               map[string]int
	bounds              []time.Duration
	details             []time.Time
	violations          []string
	accountStall        string
	accountCalls        int
}

func r5Body(path string, n int) string {
	switch path {
	case "/v1/user/login":
		return `{"code":0,"data":{"accessToken":"review5-token"}}`
	case "/v1/user/current-profile", "/v1/device-p2p/stop":
		return `{"code":200,"data":null}`
	case "/v1/my-device":
		return `{"code":200,"data":{"items":[{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}],"total":1}}`
	case "/v1/my-device/detail":
		return `{"code":200,"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`
	case "/v1/device-p2p/online-link":
		return fmt.Sprintf(`{"code":200,"data":{"conId":"round5-%d","deviceUid":"EXAMPLEUID0000000000","sdkLicenseKey":"fixture","username":"sim-user","password":"sim-pass"}}`, n)
	default:
		panic("unrecognized fixture endpoint")
	}
}
func (c *r5Cloud) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.requests++
	off := c.offline.Load()
	path := r.URL.Path
	if path == "/v1/my-device/detail" && off {
		c.details = append(c.details, time.Now())
	}
	if path == "/v1/device-p2p/online-link" && !off {
		if c.live.Load() != 0 {
			c.violations = append(c.violations, "new allocation while prior wire live")
		}
		c.allocated++
	}
	n := c.allocated
	if path == "/v1/device-p2p/stop" {
		var b struct {
			ID string `json:"conId"`
		}
		if err := json.UnmarshalRead(r.Body, &b, json.MatchCaseInsensitiveNames(true)); err != nil {
			panic(err)
		}
		c.stops[b.ID]++
		if c.stops[b.ID] != 1 || b.ID != fmt.Sprintf("round5-%d", n) || c.live.Load() != 0 {
			c.violations = append(c.violations, "duplicate/stale/live release")
		}
	}
	stall := false
	if path == c.accountStall {
		c.accountCalls++
		stall = c.accountCalls == 1
	}
	c.mu.Unlock()
	if off || stall {
		begin := time.Now()
		deadline, _ := r.Context().Deadline()
		<-r.Context().Done()
		// A finite scheduler/transport teardown delay, with no received HTTP response.
		if stall {
			<-time.After(20 * time.Millisecond)
		}
		if path == "/v1/device-p2p/stop" {
			c.mu.Lock()
			c.bounds = append(c.bounds, time.Since(begin))
			if deadline.Sub(begin) != 5*time.Second {
				c.violations = append(c.violations, fmt.Sprintf("stop budget=%v want 5s", deadline.Sub(begin)))
			}
			c.mu.Unlock()
		}
		return nil, r.Context().Err()
	}
	body := r5Body(path, n)
	if path == "/v1/my-device" && r.URL.Query().Get("current") != "1" {
		return nil, errors.New("unexpected catalog page")
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

type r5Wire struct {
	wireRunner
	c *r5Cloud
	t *testing.T
}

func (w r5Wire) Run(ctx context.Context, a <-chan iotc.Action, emit func(iotc.Event) bool) {
	if n := w.c.live.Add(1); n != 1 {
		w.t.Errorf("live wire owners=%d want 1", n)
	}
	defer w.c.live.Add(-1)
	w.wireRunner.Run(ctx, a, emit)
}
func r5Session(t *testing.T, ctx context.Context, client *cloudhttp.Client, c *r5Cloud) *Session {
	t.Helper()
	s, err := start(ctx, Config{}, func(s *Session) sessionedge.Factory {
		return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
			trace := []string{}
			w := newSimulatedWire(t, &trace)
			w.outage = c.offline.Load
			return r5Wire{w, c, t}
		}}
	})
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	return s
}
func r5Credentials(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "review@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "synthetic-password")
}
func TestAccountRetryAfterIndependentOutage(t *testing.T) {
	for _, duration := range []time.Duration{149 * time.Second, 18 * time.Hour} {
		t.Run(duration.String(), func(t *testing.T) {
			r5Credentials(t)
			synctest.Test(t, func(t *testing.T) {
				c := &r5Cloud{stops: map[string]int{}}
				var s *Session
				// Both the account owner and every session/transport descendant
				// inherit this test's label; the calling test restores its labels.
				pprof.Do(t.Context(), pprof.Labels("outage_test", t.Name()), func(ctx context.Context) {
					client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: c, StateDir: t.TempDir()})
					if err != nil {
						t.Fatal(err)
					}
					s = r5Session(t, ctx, client, c)
				})
				defer s.Close()
				if n := ownedWorkerCount(t, "outage_test", t.Name()); n == 0 {
					t.Fatal("live session missing from owned worker inventory")
				}
				conn, err := s.DialPort(t.Context(), 4700)
				if err != nil {
					t.Fatal(err)
				}
				if err = exchange(conn.(*stream), 91, 64); err != nil {
					t.Fatal(err)
				}
				c.offline.Store(true)
				<-time.After(duration)
				synctest.Wait()
				select {
				case <-s.Done():
					t.Fatal("daemon exited during recoverable outage")
				default:
				}
				c.mu.Lock()
				requests := c.requests
				if c.stops["round5-1"] != 1 || len(c.bounds) != 1 || c.bounds[0] != 5*time.Second {
					t.Errorf("old stop=%v bounds=%v want exactly once/5s", c.stops, c.bounds)
				}
				different := map[time.Duration]bool{}
				for i := 1; i < len(c.details); i++ {
					delay := c.details[i].Sub(c.details[i-1]) - 30*time.Second
					if delay < 500*time.Millisecond || delay > 30*time.Second {
						t.Errorf("retry delay=%v want 0.5..30s", delay)
					}
					if i > 6 {
						if delay < 15*time.Second {
							t.Errorf("capped equal jitter=%v want 15..30s", delay)
						}
						different[delay] = true
					}
				}
				if duration > time.Hour && (requests < 900 || requests > 2500 || len(different) < 20) {
					t.Errorf("18h requests=%d distinct capped delays=%d want 900..2500/20+", requests, len(different))
				}
				c.mu.Unlock()
				if c.live.Load() != 0 {
					t.Fatal("old session did not quiesce")
				}
				conn.Close()
				c.offline.Store(false)
				<-time.After(100 * time.Second)
				synctest.Wait()
				conn, err = s.DialPort(t.Context(), 4700)
				if err != nil {
					t.Fatal(err)
				}
				if err = exchange(conn.(*stream), 92, 2048); err != nil {
					t.Fatal(err)
				}
				conn.Close()
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				c.mu.Lock()
				defer c.mu.Unlock()
				if c.allocated != 2 || len(c.stops) != 2 || c.stops["round5-2"] != 1 || len(c.violations) != 0 || c.live.Load() != 0 {
					t.Errorf("allocations=%d stops=%v violations=%v live=%d", c.allocated, c.stops, c.violations, c.live.Load())
				}
				if n := ownedWorkerCount(t, "outage_test", t.Name()); n != 0 {
					buf := make([]byte, 1<<20)
					used := runtime.Stack(buf, true)
					t.Errorf("owned goroutines after join=%d want 0\n%s", n, buf[:used])
				}
				t.Logf("outage=%v requests=%d capped jitter values=%d allocations=%d stops=%v stop bounds=%v", duration, requests, len(different), c.allocated, c.stops, c.bounds)
			})
		})
	}
}
func TestAccountRetryCancellationDuringBackoff(t *testing.T) {
	r5Credentials(t)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		c := &r5Cloud{stops: map[string]int{}}
		client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: c, StateDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		s := r5Session(t, ctx, client, c)
		defer s.Close()
		conn, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		c.offline.Store(true)
		for i := 0; i < 10000; i++ {
			<-time.After(10 * time.Millisecond)
			synctest.Wait()
			c.mu.Lock()
			finished := len(c.bounds) == 1
			before := c.requests
			c.mu.Unlock()
			if !finished {
				continue
			}
			begin := time.Now()
			cancel()
			err = s.Close()
			synctest.Wait()
			c.mu.Lock()
			defer c.mu.Unlock()
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
			if time.Since(begin) > 5*time.Second || c.requests != before || c.allocated != 1 || c.stops["round5-1"] != 1 || c.live.Load() != 0 {
				t.Errorf("backoff cancellation elapsed=%v requests=%d->%d stops=%v", time.Since(begin), before, c.requests, c.stops)
			}
			t.Logf("backoff cancellation=%v one joined stop", time.Since(begin))
			return
		}
		t.Fatal("never reached backoff")
	})
}

// No status, body or credential rejection is observed: the only fault is a
// request that remains unreachable until its default 30s deadline expires.
func TestAccountNetworkDeadlineRecovers(t *testing.T) {
	for _, path := range []string{"/v1/user/login", "/v1/user/current-profile"} {
		t.Run(path, func(t *testing.T) {
			r5Credentials(t)
			synctest.Test(t, func(t *testing.T) {
				c := &r5Cloud{stops: map[string]int{}, accountStall: path}
				client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: c, StateDir: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				s := r5Session(t, t.Context(), client, c)
				defer s.Close()
				<-time.After(180 * time.Second)
				synctest.Wait()
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				conn, dialErr := s.DialPort(ctx, 4700)
				if conn != nil {
					conn.Close()
				}
				c.mu.Lock()
				calls, allocated := c.accountCalls, c.allocated
				c.mu.Unlock()
				t.Logf("default30s %s calls=%d allocations=%d client=%v", path, calls, allocated, dialErr)
				if dialErr != nil || allocated != 1 {
					t.Errorf("pure network deadline permanently blocked recovery: client=%v allocations=%d want admitted/1", dialErr, allocated)
				}
				// A direct later caller proves owner completion has become safely retryable.
				if control := client.Login(t.Context()); control != nil {
					t.Errorf("owner control after join=%v want nil", control)
				}
				if errors.Is(dialErr, session.ErrUncertain) {
					t.Log("daemon retained uncertainty after safe owner completion")
				}
			})
		})
	}
}

func TestStartupLoginTimeoutRetried(t *testing.T) {
	r5Credentials(t)
	synctest.Test(t, func(t *testing.T) {
		c := &r5Cloud{stops: map[string]int{}, accountStall: "/v1/user/login"}
		client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: c, StateDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		d, err := selectDevice(t.Context(), client, cloudhttp.Device{}, nil, mathrand.NewPCG(17, 29))
		t.Logf("default selector startup returned after %d login calls: selection=%v error=%v", c.accountCalls, d, err)
		if err != nil || d.Serial != "sim" {
			t.Errorf("startup aborted on recoverable login timeout: error=%v selected=%v want nil/sim", err, d)
		}
		<-time.After(40 * time.Millisecond)
		synctest.Wait()
		if control := client.Login(t.Context()); control != nil {
			t.Errorf("later joined owner control=%v want nil", control)
		}
	})
}
