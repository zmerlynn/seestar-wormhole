package proxy

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"runtime"
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

// Independent schedules and oracle; only the existing synthetic wire peer is reused.
type independentOutageCloud struct {
	offline       atomic.Bool
	live          atomic.Int32
	mu            sync.Mutex
	calls         []time.Time
	allocs        int
	stops         map[string]int
	stopDurations []time.Duration
	violations    []string
}

func cloudResponse(s string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(s))}
}
func (c *independentOutageCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.calls = append(c.calls, time.Now())
	path := r.URL.Path
	if path == "/v1/device-p2p/online-link" && !c.offline.Load() {
		if c.live.Load() != 0 {
			c.violations = append(c.violations, "allocation while old wire is live")
		}
		c.allocs++
		id := c.allocs
		c.mu.Unlock()
		return cloudResponse(fmt.Sprintf(`{"code":200,"data":{"conId":"review-%d","deviceUid":"EXAMPLEUID0000000000","sdkLicenseKey":"fixture","username":"sim-user","password":"sim-pass"}}`, id)), nil
	}
	if path == "/v1/device-p2p/stop" {
		var b struct {
			ID string `json:"conId"`
		}
		if err := json.UnmarshalRead(r.Body, &b, json.MatchCaseInsensitiveNames(true)); err != nil {
			panic(err)
		}
		if c.stops == nil {
			c.stops = map[string]int{}
		}
		c.stops[b.ID]++
		if c.stops[b.ID] != 1 {
			c.violations = append(c.violations, "duplicate stop "+b.ID)
		}
		if c.live.Load() != 0 {
			c.violations = append(c.violations, "stop racing live wire")
		}
		if b.ID != fmt.Sprintf("review-%d", c.allocs) {
			c.violations = append(c.violations, "stale stop against newer allocation")
		}
	}
	off := c.offline.Load()
	c.mu.Unlock()
	if off {
		start := time.Now()
		<-r.Context().Done()
		if path == "/v1/device-p2p/stop" {
			c.mu.Lock()
			c.stopDurations = append(c.stopDurations, time.Since(start))
			c.mu.Unlock()
		}
		return nil, r.Context().Err()
	}
	switch path {
	case "/v1/my-device/detail":
		return cloudResponse(`{"code":200,"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`), nil
	case "/v1/device-p2p/stop":
		return cloudResponse(`{"code":200,"data":null}`), nil
	}
	return nil, errors.New("unexpected review request")
}

type reviewWire struct {
	wireRunner
	cloud *independentOutageCloud
	t     *testing.T
}

func (w reviewWire) Run(ctx context.Context, actions <-chan iotc.Action, emit func(iotc.Event) bool) {
	if n := w.cloud.live.Add(1); n != 1 {
		w.t.Errorf("concurrent device wire sessions=%d, want 1", n)
	}
	defer w.cloud.live.Add(-1)
	w.wireRunner.Run(ctx, actions, emit)
}
func outageSession(t *testing.T, ctx context.Context, c *independentOutageCloud) *Session {
	client, err := newFixtureClient(t, cloudhttp.Config{Transport: c})
	if err != nil {
		t.Fatal(err)
	}
	s, err := start(ctx, Config{}, func(s *Session) sessionedge.Factory {
		return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
			trace := []string{}
			p := newSimulatedWire(t, &trace)
			p.outage = c.offline.Load
			return reviewWire{p, c, t}
		}}
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestIndependentCloudOutage(t *testing.T) {
	for _, duration := range []time.Duration{173 * time.Second, 8 * time.Hour} {
		t.Run(duration.String(), func(t *testing.T) {

			synctest.Test(t, func(t *testing.T) {
				base := runtime.NumGoroutine()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cloud := &independentOutageCloud{}
				s := outageSession(t, ctx, cloud)
				defer s.Close()
				c, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatal(err)
				}
				if err := exchange(c.(*stream), 41, 0); err != nil {
					t.Fatal(err)
				}
				cloud.offline.Store(true)
				<-time.After(duration)
				synctest.Wait()
				select {
				case <-s.Done():
					t.Fatalf("daemon died in recoverable outage: %v", s.Close())
				default:
				}
				cloud.mu.Lock()
				requests := len(cloud.calls)
				if len(cloud.stops) != 1 || cloud.stops["review-1"] != 1 {
					t.Errorf("outage stops=%v, want review-1 once", cloud.stops)
				}
				if len(cloud.stopDurations) != 1 || cloud.stopDurations[0] != 5*time.Second {
					t.Errorf("failed stop bounds=%v, want [5s]", cloud.stopDurations)
				}
				cloud.mu.Unlock()
				if duration >= time.Hour && (requests < 400 || requests > 2000) {
					t.Errorf("8h unreachable requests=%d, want 400..2000 (no spin)", requests)
				}
				if cloud.live.Load() != 0 {
					t.Fatalf("old wire still alive after outage")
				}
				c.Close()
				cloud.offline.Store(false)
				// Retire an in-flight request that began offline, then its capped retry.
				<-time.After(70 * time.Second)
				synctest.Wait()
				recovered, cancelRecovery := context.WithTimeout(ctx, 100*time.Second)
				c2, err := s.DialPort(recovered, 4700)
				cancelRecovery()
				if err != nil {
					t.Fatalf("recovery: %v", err)
				}
				if err := exchange(c2.(*stream), 42, 1024); err != nil {
					t.Fatal(err)
				}
				c2.Close()
				if err := s.Close(); err != nil {
					t.Fatalf("final close: %v", err)
				}
				synctest.Wait()
				cloud.mu.Lock()
				if cloud.allocs != 2 || cloud.stops["review-2"] != 1 || len(cloud.stops) != 2 || len(cloud.violations) > 0 {
					t.Errorf("allocs=%d stops=%v violations=%v", cloud.allocs, cloud.stops, cloud.violations)
				}
				t.Logf("outage=%v requests=%d allocations=%d stops=%v timeout=%v", duration, requests, cloud.allocs, cloud.stops, cloud.stopDurations)
				cloud.mu.Unlock()
				if n := runtime.NumGoroutine(); n > base {
					t.Logf("global harness goroutines=%d -> %d; check owned workers below", base, n)
				}
				buf := make([]byte, 65536)
				size := runtime.Stack(buf, true)
				for _, worker := range strings.Split(string(buf[:size]), "\n\n") {
					for _, name := range []string{"proxy.(*stack).", "proxy.reviewWire.Run", "sessionedge.attempt(", "cloudhttp.newAccountOwner.", "github.com/pion/dtls/v3."} {
						if strings.Contains(worker, name) {
							t.Errorf("owned worker survived close: %s", worker)
						}
					}
				}
			})
		})
	}
}
func TestSignalDuringBackoff(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cloud := &independentOutageCloud{}
		s := outageSession(t, ctx, cloud)
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		// A blocked RPC forces transport detection; poll virtual time to stop in the
		// interval after cloud stop completes and before the next detail request.
		cloud.offline.Store(true)
		deadline := time.Now().Add(180 * time.Second)
		for {
			<-time.After(10 * time.Millisecond)
			synctest.Wait()
			cloud.mu.Lock()
			done := len(cloud.stopDurations) == 1
			calls := len(cloud.calls)
			cloud.mu.Unlock()
			if done {
				start := time.Now()
				cancel()
				err := s.Close()
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Errorf("cancel during backoff error=%v", err)
				}
				if time.Since(start) > 5*time.Second {
					t.Errorf("backoff cancellation bound=%v", time.Since(start))
				}
				cloud.mu.Lock()
				if len(cloud.calls) != calls || cloud.stops["review-1"] != 1 {
					t.Errorf("signal started new traffic: calls %d -> %d stops=%v", calls, len(cloud.calls), cloud.stops)
				}
				cloud.mu.Unlock()
				t.Logf("signal during backoff joined in %v; old release remained single", time.Since(start))
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("no release/backoff reached")
			}
		}
	})
}
func TestJitterBounds(t *testing.T) {
	c := session.DefaultConfig()
	rng := rand.NewPCG(41, 19)
	var cap time.Duration
	seen := map[time.Duration]bool{}
	for i := 0; i < 10000; i++ {
		delay, next := session.RetryBackoff(c, rng, cap)
		if delay < c.BackoffMin || delay > c.BackoffMax {
			t.Fatalf("delay=%v outside [%v,%v]", delay, c.BackoffMin, c.BackoffMax)
		}
		if cap == c.BackoffMax {
			if delay < c.BackoffMax/2 {
				t.Fatalf("equal jitter floor=%v", delay)
			}
			seen[delay] = true
		}
		cap = next
	}
	if len(seen) < 9000 {
		t.Errorf("constant/low diversity jitter: %d distinct", len(seen))
	}
}

type unknownCleanupCloud struct{ online atomic.Int32 }

func (tr *unknownCleanupCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.URL.Path {
	case "/v1/my-device/detail":
		return cloudResponse(`{"code":200,"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`), nil
	case "/v1/device-p2p/online-link":
		tr.online.Add(1)
		return cloudResponse(`{"code":7788,"data":{"conId":"unknown-allocation"}}`), nil
	case "/v1/device-p2p/stop":
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	return nil, errors.New("unexpected")
}
func TestUnknownCleanupKeepsUncertainty(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		c, err := newFixtureClient(t, cloudhttp.Config{Transport: &unknownCleanupCloud{}})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		a := allocator{c, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
		_, err = a.Allocate(context.Background())
		if !errors.Is(err, session.ErrUncertain) {
			t.Fatalf("unknown/malformed allocation plus failed cleanup=%v, want ErrUncertain", err)
		}
	})
}

func TestMalformedCleanupDaemonDoesNotReacquire(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		tr := &unknownCleanupCloud{}
		c, err := newFixtureClient(t, cloudhttp.Config{Transport: tr})
		if err != nil {
			t.Fatal(err)
		}
		s, err := start(context.Background(), Config{}, func(s *Session) sessionedge.Factory {
			return &factory{s: s, cloud: c, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		<-time.After(time.Minute)
		synctest.Wait()
		if n := tr.online.Load(); n != 1 {
			t.Fatalf("online-link calls after malformed allocation+failed stop=%d, want 1 (operator-blocked)", n)
		}
	})
}
func TestSignalWithOwedRelease(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cloud := &independentOutageCloud{}
		s := outageSession(t, ctx, cloud)
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		cloud.offline.Store(true)
		start := time.Now()
		cancel()
		synctest.Wait()
		err = s.Close()
		if !errors.Is(err, session.ErrStop) {
			t.Errorf("final owed release failure=%v, want ErrStop", err)
		}
		if d := time.Since(start); d != 5*time.Second {
			t.Errorf("final release elapsed=%v, want 5s", d)
		}
		cloud.mu.Lock()
		defer cloud.mu.Unlock()
		if cloud.stops["review-1"] != 1 || cloud.allocs != 1 || len(cloud.violations) > 0 {
			t.Errorf("signal allocs=%d stops=%v violations=%v", cloud.allocs, cloud.stops, cloud.violations)
		}
		t.Logf("signal with owed release completed in %v with stops=%v", time.Since(start), cloud.stops)
	})
}
